// area_drop.js runs web/static/js/app.js in Node with a small fake DOM, drops an area file, and prints the area list as JSON.
// Usage: node area_drop.js <app.js> <server URL> <area file>
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

// fakeLayer stands in for a Leaflet layer. Only toGeoJSON returns data, because the area list sends the geometry.
function fakeLayer(geometry) {
    return {
        addTo() { return this; },
        on() { return this; },
        setStyle() { return this; },
        bringToFront() { return this; },
        getBounds() { return { isValid: () => true }; },
        toGeoJSON() { return { type: 'Feature', geometry }; }
    };
}

const map = {
    removeLayer() {},
    fitBounds() {},
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
    latLngBounds: () => ({ isValid: () => true })
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
    const file = new File([fs.readFileSync(areaPath)], path.basename(areaPath));
    await app.onAreaDrop({ preventDefault() {}, dataTransfer: { files: [file] } });
    const rows = elements.areaList.children;
    process.stdout.write(JSON.stringify({
        rows: rows.length,
        names: app.areas.map(a => a.name),
        kinds: app.areas.map(a => a.kind),
        tiles: app.areas.map(a => a.tiles),
        rowText: rows.map(r => r.textContent),
        total: elements.areaTotal.textContent,
        empty: elements.areaEmpty.hidden,
        error: elements.errorBanner.hidden ? '' : elements.errorMessage.textContent
    }));
})().catch(error => {
    process.stderr.write(String(error && error.stack || error));
    process.exit(1);
});
