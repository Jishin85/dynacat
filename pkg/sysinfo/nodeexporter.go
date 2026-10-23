package sysinfo

import (
	"bufio"
	"cmp"
	"io"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/shirou/gopsutil/v4/sensors"
)

// Collectors that expose the metrics read below, passed as collect[] so node_exporter skips the rest.
var NodeExporterCollectors = []string{"cpu", "filesystem", "hwmon", "loadavg", "meminfo", "os", "stat", "uname"}

// Filesystems node_exporter reports by default that the local collector leaves out.
var nodeExporterIgnoredFSTypes = map[string]bool{"tmpfs": true, "ramfs": true}

type nodeExporterFS struct {
	fsType            string
	size, free, avail float64
	hasSize, hasAvail bool
}

type nodeExporterTemp struct {
	chip, sensor string
	celsius      float64
}

// Streams a node_exporter /metrics response and keeps only the values the widget renders.
func ParseNodeExporter(r io.Reader, req *SystemInfoRequest) (*SystemInfo, []error) {
	if req == nil {
		req = &SystemInfoRequest{}
	}

	var errs []error
	values := map[string]float64{}
	filesystems := map[string]*nodeExporterFS{}
	chipNames := map[string]string{}
	sensorLabels := map[string]string{}
	var temps []nodeExporterTemp
	var hostname, sysname, osID string
	cores := 0

	fs := func(labels map[string]string) *nodeExporterFS {
		mp := labels["mountpoint"]
		if filesystems[mp] == nil {
			filesystems[mp] = &nodeExporterFS{fsType: labels["fstype"]}
		}
		return filesystems[mp]
	}

	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" || line[0] == '#' {
			continue
		}

		nameEnd := strings.IndexAny(line, "{ ")
		if nameEnd < 0 {
			continue
		}

		name := line[:nameEnd]
		switch name {
		case "node_boot_time_seconds", "node_load1", "node_load15",
			"node_memory_MemTotal_bytes", "node_memory_MemAvailable_bytes",
			"node_memory_SwapTotal_bytes", "node_memory_SwapFree_bytes",
			"node_cpu_seconds_total", "node_uname_info", "node_os_info",
			"node_filesystem_size_bytes", "node_filesystem_free_bytes", "node_filesystem_avail_bytes",
			"node_hwmon_chip_names", "node_hwmon_sensor_label", "node_hwmon_temp_celsius":
		default:
			continue
		}

		var labels map[string]string
		rest := line[nameEnd:]
		if rest[0] == '{' {
			var ok bool
			if labels, rest, ok = parseNodeExporterLabels(rest); !ok {
				continue
			}
		}

		fields := strings.Fields(rest)
		if len(fields) == 0 {
			continue
		}

		value, err := strconv.ParseFloat(fields[0], 64)
		if err != nil {
			continue
		}

		switch name {
		case "node_cpu_seconds_total":
			if labels["mode"] == "idle" {
				cores++
			}
		case "node_uname_info":
			hostname, sysname = labels["nodename"], labels["sysname"]
		case "node_os_info":
			osID = labels["id"]
		case "node_filesystem_size_bytes":
			f := fs(labels)
			f.size, f.hasSize = value, true
		case "node_filesystem_free_bytes":
			fs(labels).free = value
		case "node_filesystem_avail_bytes":
			f := fs(labels)
			f.avail, f.hasAvail = value, true
		case "node_hwmon_chip_names":
			chipNames[labels["chip"]] = labels["chip_name"]
		case "node_hwmon_sensor_label":
			sensorLabels[labels["chip"]+"/"+labels["sensor"]] = labels["label"]
		case "node_hwmon_temp_celsius":
			temps = append(temps, nodeExporterTemp{labels["chip"], labels["sensor"], value})
		default:
			values[name] = value
		}
	}

	if err := scanner.Err(); err != nil {
		errs = append(errs, err)
	}

	info := &SystemInfo{
		Hostname:    hostname,
		Platform:    cmp.Or(osID, strings.ToLower(sysname)),
		Mountpoints: []MountpointInfo{},
	}

	if bootTime, ok := values["node_boot_time_seconds"]; ok {
		info.HostInfoIsAvailable = true
		info.BootTime = timestampJSON{time.Unix(int64(bootTime), 0)}
	}

	load1, ok1 := values["node_load1"]
	load15, ok15 := values["node_load15"]
	if ok1 && ok15 && cores > 0 {
		info.CPU.LoadIsAvailable = true
		info.CPU.Load1Percent = percent(load1, float64(cores))
		info.CPU.Load15Percent = percent(load15, float64(cores))
	}

	memTotal, okTotal := values["node_memory_MemTotal_bytes"]
	memAvail, okAvail := values["node_memory_MemAvailable_bytes"]
	if okTotal && okAvail {
		info.Memory.IsAvailable = true
		info.Memory.TotalMB = toMB(memTotal)
		info.Memory.UsedMB = toMB(memTotal - memAvail)
		info.Memory.UsedPercent = percent(memTotal-memAvail, memTotal)
	}

	swapTotal, okTotal := values["node_memory_SwapTotal_bytes"]
	swapFree, okFree := values["node_memory_SwapFree_bytes"]
	if okTotal && okFree {
		info.Memory.SwapIsAvailable = true
		info.Memory.SwapTotalMB = toMB(swapTotal)
		info.Memory.SwapUsedMB = toMB(swapTotal - swapFree)
		info.Memory.SwapUsedPercent = percent(swapTotal-swapFree, swapTotal)
	}

	if len(temps) > 0 {
		readings := make([]sensors.TemperatureStat, len(temps))
		for i, t := range temps {
			key := cmp.Or(chipNames[t.chip], t.chip)
			// Same key format as gopsutil so cpu-temp-sensor matches like on the local server.
			if label := sensorLabels[t.chip+"/"+t.sensor]; label != "" {
				key += "_" + strings.Join(strings.Fields(strings.ToLower(label)), "_")
			}
			readings[i] = sensors.TemperatureStat{SensorKey: key, Temperature: t.celsius}
		}

		if err := info.applyCPUTemp(readings, req.CPUTempSensor); err != nil {
			errs = append(errs, err)
		}
	}

	for path, f := range filesystems {
		mpReq, configured := req.Mountpoints[path]
		if !f.hasSize || !f.hasAvail || (!configured && nodeExporterIgnoredFSTypes[f.fsType]) || req.isMountpointHidden(mpReq) {
			continue
		}

		used := f.size - f.free
		info.Mountpoints = append(info.Mountpoints, MountpointInfo{
			Path:        path,
			Name:        mpReq.Name,
			TotalMB:     toMB(f.size),
			UsedMB:      toMB(used),
			UsedPercent: percent(used, used+f.avail),
		})
	}

	info.sortMountpoints(req.MountpointOrder)

	return info, errs
}

// Parses a `{key="value",...}` label set, returning the labels and what follows the closing brace.
func parseNodeExporterLabels(s string) (map[string]string, string, bool) {
	labels := map[string]string{}
	s = s[1:]

	for {
		s = strings.TrimLeft(s, ", ")
		if strings.HasPrefix(s, "}") {
			return labels, s[1:], true
		}

		eq := strings.IndexByte(s, '=')
		if eq < 0 || eq+1 >= len(s) || s[eq+1] != '"' {
			return nil, "", false
		}

		key := strings.TrimSpace(s[:eq])
		s = s[eq+2:]

		var value strings.Builder
		i := 0
		for ; i < len(s) && s[i] != '"'; i++ {
			if s[i] == '\\' && i+1 < len(s) {
				i++
				if s[i] == 'n' {
					value.WriteByte('\n')
					continue
				}
			}
			value.WriteByte(s[i])
		}

		if i >= len(s) {
			return nil, "", false
		}

		labels[key] = value.String()
		s = s[i+1:]
	}
}

func toMB(bytes float64) uint64 {
	return uint64(math.Max(bytes, 0)) / 1024 / 1024
}

func percent(part, total float64) uint8 {
	if total <= 0 {
		return 0
	}

	return uint8(math.Max(math.Min(part/total*100, 100), 0))
}
