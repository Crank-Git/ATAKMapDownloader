// area_drop.js runs web/static/js/app.js in Node with a small fake DOM, drops an area file, and prints the area list and the map moves as JSON.
// Usage: node area_drop.js <app.js> <server URL> <area file>
// With the area file "draw", the script draws a polygon with the Polygon tool instead of a drop.
// The fetch calls of app.js go to the server URL, so the parse, estimate and tiles routes are the real ones.
'use strict';

const fs = require('fs');
const path = require('path');
const vm = require('vm');

const [appPath, baseURL, areaPath] = process.argv.slice(2);

// FakeElement holds the parts of a DOM element that the area list code uses.
class FakeElement {
    constructor(tag) {
        this.tagName = tag;
        this.children = [];
        this.attributes = {};
        this.listeners = {};
        this.hidden = false;
        this.disabled = false;
        this.value = '';
        this.className = '';
        this.type = '';
        this.min = '';
        this.max = '';
        this.style = {};
        this.dataset = {};
        this.text = '';
        this.classList = {
            toggle: (name, on) => {
                const names = new Set(this.className.split(' ').filter(Boolean));
                if (on === undefined ? !names.has(name) : on) names.add(name); else names.delete(name);
                this.className = [...names].join(' ');
            },
            add: (name) => this.classList.toggle(name, true),
            remove: (name) => this.classList.toggle(name, false),
            contains: (name) => this.className.split(' ').includes(name)
        };
    }
    get textContent() {
        return this.text + this.children.map(c => (typeof c === 'string' ? c : c.textContent)).join('');
    }
    set textContent(v) {
        this.text = String(v);
        this.children = [];
    }
    set innerHTML(_) {
        this.text = '';
        this.children = [];
    }
    get firstChild() { return this.children[0]; }
    append(...nodes) { this.children.push(...nodes); }
    appendChild(node) { this.children.push(node); return node; }
    removeChild(node) { this.children = this.children.filter(c => c !== node); }
    replaceChildren(...nodes) { this.text = ''; this.children = nodes; }
    setAttribute(name, value) { this.attributes[name] = String(value); }
    getAttribute(name) { return name in this.attributes ? this.attributes[name] : null; }
    addEventListener(type, fn) { (this.listeners[type] = this.listeners[type] || []).push(fn); }
    querySelector() { return null; }
    querySelectorAll() { return []; }
    insertAdjacentHTML() {}
    scrollIntoView() {}
    remove() {}
    click() {}
}

const elements = {};
const document = {
    getElementById: (id) => (elements[id] = elements[id] || new FakeElement('div')),
    createElement: (tag) => new FakeElement(tag),
    addEventListener: () => {},
    documentElement: new FakeElement('html')
};
// index.html marks the banner hidden, so the fake starts the same way.
document.getElementById('errorBanner').hidden = true;

// FakeBounds holds the parts of L.LatLngBounds that the import code uses. With no corners it is empty, as L.latLngBounds() is.
class FakeBounds {
    constructor(...corners) {
        this.south = Infinity;
        this.west = Infinity;
        this.north = -Infinity;
        this.east = -Infinity;
        for (const [lat, lng] of corners) this.extendPoint(lat, lng);
    }
    extendPoint(lat, lng) {
        this.south = Math.min(this.south, lat);
        this.west = Math.min(this.west, lng);
        this.north = Math.max(this.north, lat);
        this.east = Math.max(this.east, lng);
    }
    extend(other) {
        if (other.isValid()) {
            this.extendPoint(other.south, other.west);
            this.extendPoint(other.north, other.east);
        }
        return this;
    }
    isValid() { return this.south <= this.north; }
}

// geometryBounds returns the bounds of the GeoJSON positions in geometry, which Leaflet gets from the layer.
function geometryBounds(geometry) {
    const bounds = new FakeBounds();
    const walk = (c) => (typeof c[0] === 'number' ? bounds.extendPoint(c[1], c[0]) : c.forEach(walk));
    if (geometry) walk(geometry.coordinates);
    return bounds;
}

// fakeLayer stands in for a Leaflet layer. toGeoJSON returns the geometry, because the area list sends it.
function fakeLayer(geometry) {
    return {
        addTo() { return this; },
        on() { return this; },
        setStyle() { return this; },
        bringToFront() { return this; },
        getBounds() { return geometryBounds(geometry); },
        toGeoJSON() { return { type: 'Feature', geometry }; }
    };
}

// fitBoundsCalls records each map move, so the check can see where the import put the map.
const fitBoundsCalls = [];
const map = {
    removeLayer() {},
    fitBounds(bounds, options) {
        fitBoundsCalls.push({ bounds: [bounds.west, bounds.south, bounds.east, bounds.north], options: options || {} });
    },
    pm: {
        disableDraw() {},
        disableGlobalEditMode() {},
        disableGlobalDragMode() {},
        disableGlobalRemovalMode() {},
        controlsVisible() { return true; },
        toggleControls() {}
    }
};

const L = {
    geoJSON: (geometry) => ({ getLayers: () => [fakeLayer(geometry)] }),
    layerGroup: () => ({ addTo() { return this; } }),
    rectangle: () => fakeLayer(null),
    latLngBounds: (...corners) => new FakeBounds(...corners)
};

const context = vm.createContext({
    document,
    window: { location: {} },
    navigator: { onLine: true },
    L,
    fetch: (p, options) => fetch(new URL(p, baseURL), options),
    FormData,
    File,
    URL,
    URLSearchParams,
    console,
    setTimeout,
    clearTimeout,
    setInterval,
    clearInterval
});
vm.runInContext(fs.readFileSync(appPath, 'utf8'), context, { filename: appPath });
const ATAKDownloader = vm.runInContext('ATAKDownloader', context);

(async () => {
    // init builds the Leaflet map and loads the sources, which this check does not need.
    ATAKDownloader.prototype.init = function () {};
    const app = new ATAKDownloader();
    app.map = map;
    if (areaPath === 'draw') {
        // onDrawCreate does not return the estimate request, so the script waits for each request that it starts.
        const pending = [];
        const areasChanged = app.areasChanged.bind(app);
        app.areasChanged = () => { const p = areasChanged(); pending.push(p); return p; };
        const ring = [[-121.8, 46.9], [-121.7, 46.9], [-121.7, 46.95], [-121.8, 46.9]];
        app.onDrawCreate({ shape: 'Polygon', layer: fakeLayer({ type: 'Polygon', coordinates: [ring] }) });
        await Promise.all(pending);
    } else {
        const file = new File([fs.readFileSync(areaPath)], path.basename(areaPath));
        await app.onAreaDrop({ preventDefault() {}, dataTransfer: { files: [file] } });
    }
    const rows = document.getElementById('areaList').children;
    process.stdout.write(JSON.stringify({
        rows: rows.length,
        names: app.areas.map(a => a.name),
        kinds: app.areas.map(a => a.kind),
        tiles: app.areas.map(a => a.tiles),
        rowText: rows.map(r => r.textContent),
        total: document.getElementById('areaTotal').textContent,
        empty: document.getElementById('areaEmpty').hidden,
        error: document.getElementById('errorBanner').hidden ? '' : document.getElementById('errorMessage').textContent,
        fitBounds: fitBoundsCalls
    }));
})().catch(error => {
    process.stderr.write(String(error && error.stack || error));
    process.exit(1);
});
