import { clamp } from "./utils.js";

export function setupMasonries() {
    const masonryContainers = document.getElementsByClassName("masonry");

    for (let i = 0; i < masonryContainers.length; i++) {
        const container = masonryContainers[i];

        if (container.dataset.initialized) continue;
        container.dataset.initialized = "true";

        const options = {
            minColumnWidth: container.dataset.minColumnWidth || 330,
            maxColumns: container.dataset.maxColumns || 6,
        };

        let previousColumnsCount = 0;
        // Re-read items on each render since live updates replace the masonry children.
        const collectState = () => {
            const columns = [];
            const loose = [];

            for (const child of container.children) {
                (child.classList.contains("masonry-column") ? columns : loose).push(child);
            }

            // Traverse columns row by row to restore the original round-robin item order.
            const items = [];
            const rows = Math.max(0, ...columns.map(c => c.children.length));

            for (let row = 0; row < rows; row++) {
                for (const column of columns) {
                    if (row < column.children.length) {
                        items.push(column.children[row]);
                    }
                }
            }

            return {
                items: items.concat(loose),
                hasColumns: columns.length > 0,
                hasLoose: loose.length > 0,
            };
        };

        const render = function() {
            const { items, hasColumns, hasLoose } = collectState();

            const columnsCount = clamp(
                Math.floor(container.offsetWidth / options.minColumnWidth),
                1,
                Math.min(options.maxColumns, items.length)
            );

            // Skip rebuilding only if columns exist, all items are wrapped, and the count matches.
            if (hasColumns && !hasLoose && columnsCount === previousColumnsCount) {
                return;
            }

            container.textContent = "";
            previousColumnsCount = columnsCount;

            const columnsFragment = document.createDocumentFragment();

            for (let i = 0; i < columnsCount; i++) {
                const column = document.createElement("div");
                column.className = "masonry-column";
                columnsFragment.append(column);
            }

            for (let i = 0; i < items.length; i++) {
                columnsFragment.children[i % columnsCount].appendChild(items[i]);
            }

            container.append(columnsFragment);
        };

        // A morph reuses the container while wiping data-initialized, so it drops the previous observer.
        if (container._masonryObserver) {
            container._masonryObserver.disconnect();
        }

        // Render synchronously to avoid flashing on live updates, leaving hidden containers to the observer.
        if (container.offsetWidth > 0) {
            render();
        }

        const observer = new ResizeObserver(() => requestAnimationFrame(render));
        container._masonryObserver = observer;
        observer.observe(container);
    }
}
