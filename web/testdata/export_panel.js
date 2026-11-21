// export_panel.js runs web/static/js/app.js in Node with a small fake DOM, applies one scenario and prints the export panel as JSON.
// Usage: node export_panel.js <index.html> <app.js> <scenario JSON>
// The fake DOM takes the controls and their defaults from index.html, so the scenario starts from the real default state.
'use strict';

const fs = require('fs');
const vm = require('vm');

const [indexPath, appPath, scenarioJSON] = process.argv.slice(2);
const scenario = JSON.parse(scenarioJSON);

const elements = {};

// FakeElement holds the parts of a DOM element that the export panel and the output file list use.
class FakeElement {
    constructor(tag, attrs) {
        this.tagName = tag;
        this.attrs = attrs || {};
        this.id = this.attrs.id || '';
        this.type = this.attrs.type || '';
        this.name = this.attrs.name || '';
        this.value = this.attrs.value || '';
        this.checkedState = 'checked' in this.attrs;
        this.disabled = 'disabled' in this.attrs;
        this.hidden = 'hidden' in this.attrs;
        this.children = [];
        this.listeners = {};
        this.style = {};
        this.text = '';
        this.className = '';
        this.title = '';
        this.classList = { add() {}, remove() {}, toggle() {}, contains: () => false };
    }
    get checked() { return this.checkedState; }
    // A checked radio clears the other radios of its group, the same as a browser does.
    set checked(on) {
        if (on && this.type === 'radio') {
            Object.values(elements).forEach(e => { if (e.type === 'radio' && e.name === this.name) e.checkedState = false; });
        }
        this.checkedState = Boolean(on);
    }
    get textContent() {
        return this.text + this.children.map(c => (typeof c === 'string' ? c : c.textContent)).join('');
    }
    set textContent(v) { this.text = String(v); this.children = []; }
    set innerHTML(_) { this.text = ''; this.children = []; }
    get options() { return this.children; }
    append(...nodes) { this.children.push(...nodes); }
    appendChild(node) { this.children.push(node); return node; }
    removeChild(node) { this.children = this.children.filter(c => c !== node); }
    replaceChildren(...nodes) { this.text = ''; this.children = nodes.filter(Boolean); }
    setAttribute(name, value) { this.attrs[name] = String(value); }
    addEventListener(type, fn) { (this.listeners[type] = this.listeners[type] || []).push(fn); }
    dispatch(type) { (this.listeners[type] || []).forEach(fn => fn({ target: this })); }
    querySelector() { return null; }
}

// The attributes of a tag, with a boolean attribute as an empty string.
function attributes(tag) {
    const attrs = {};
    for (const m of tag.matchAll(/([a-z-]+)(?:="([^"]*)")?/g)) attrs[m[1]] = m[2] === undefined ? '' : m[2];
    return attrs;
}

const index = fs.readFileSync(indexPath, 'utf8');
for (const m of index.matchAll(/<(input|select|fieldset|p|span|code|div|section|button|ul)\b([^>]*\bid="[^"]+"[^>]*)>/g)) {
    const el = new FakeElement(m[1], attributes(m[2]));
    elements[el.id] = el;
}
// A select takes the value of its selected option, or of its first option.
for (const m of index.matchAll(/<select\b[^>]*\bid="([^"]+)"[^>]*>([\s\S]*?)<\/select>/g)) {
    const options = [...m[2].matchAll(/<option\b([^>]*)>/g)].map(o => attributes(o[1]));
    const selected = options.find(o => 'selected' in o) || options[0];
    elements[m[1]].value = selected ? selected.value : '';
    elements[m[1]].children = options.slice(0, 1).map(o => new FakeElement('option', o));
}

const document = {
    getElementById(id) {
        if (!elements[id]) throw new Error('index.html has no element with the id ' + id);
        return elements[id];
    },
    createElement: tag => new FakeElement(tag),
    createTextNode: text => text,
    addEventListener() {},
    documentElement: new FakeElement('html')
};
const context = vm.createContext({ document, window: {}, navigator: { onLine: true }, console });
vm.runInContext(fs.readFileSync(appPath, 'utf8'), context, { filename: appPath });
vm.runInContext('globalThis.ATAKDownloader = ATAKDownloader;', context);

// The constructor runs with no init, because init needs Leaflet and the server.
const App = context.ATAKDownloader;
App.prototype.init = function () {};
const app = new App();
const requests = [];
app.logStatus = () => {};
app.followJob = () => {};
app.postJSON = (path, body) => { requests.push({ path, body }); return Promise.resolve({ id: 'job-1', output_file: 'out' }); };
app.mapSources = scenario.sources || [];
app.startReady = true;
// bindEvents adds the drop listeners to the map container, and setJobLocked calls Geoman, but init makes no map here.
app.map = { getContainer: () => new FakeElement('div'), pm: new Proxy({}, { get: () => () => false }) };
const [west, south, east, north] = scenario.area || [];
const [minZoom, maxZoom] = scenario.zoom || [1, 15];
app.areas = scenario.area ? [{
    id: 1, kind: 'bounds', name: 'Area 1', minZoom, maxZoom, tiles: 4, layer: {},
    geometry: { type: 'Polygon', coordinates: [[[west, south], [east, south], [east, north], [west, north], [west, south]]] }
}] : [];
app.bindEvents();
app.updateExportPanel();
if (scenario.files) app.renderOutputFiles(scenario.files);

// Each setting changes in order as a user changes it, so the listeners of app.js see the input and change events.
for (const [id, v] of scenario.set || []) {
    const el = document.getElementById(id);
    if (typeof v === 'boolean') el.checked = v; else el.value = v;
    el.dispatch('input');
    el.dispatch('change');
}

(async () => {
    if (scenario.start) await app.startDownload();
    const formats = ['mbtiles', 'osmdroid', 'gpkg'];
    const out = {
        preview: elements.exportPreview.textContent,
        message: elements.packageNameMessage.hidden ? '' : elements.packageNameMessage.textContent,
        startDisabled: elements.downloadBtn.disabled,
        format: formats.find(f => elements['format-' + f].checked) || '',
        disabledFormats: formats.filter(f => elements['format-' + f].disabled),
        formatLock: elements.formatLock.hidden ? '' : elements.formatLock.textContent,
        packageOptionsDisabled: elements.packageOptions.disabled,
        request: requests.length ? requests[0].body : null,
        fileRows: elements.outputFileList.children.map(row => row.textContent),
        baseMaps: elements.baseMapSelect.children.map(o => o.value).slice(1)
    };
    process.stdout.write(JSON.stringify(out));
})().catch(err => {
    process.stderr.write(String(err && err.stack || err));
    process.exit(1);
});
