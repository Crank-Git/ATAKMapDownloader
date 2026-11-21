// ATAK Tile Downloader web application. It runs jobs through the job API and follows them over SSE.

// The icons are inline SVG constants with no server or user text, so innerHTML is safe for them (FR-offline-web-ui-4).
const ICON_DOWNLOAD = '<svg class="icon" viewBox="0 0 24 24" aria-hidden="true"><path d="M12 4v11M7 10l5 5 5-5M5 20h14"/></svg>';
const ICON_CANCEL = '<svg class="icon" viewBox="0 0 24 24" aria-hidden="true"><path d="M6 6l12 12M18 6L6 18"/></svg>';
const SPINNER = '<span class="spinner" aria-hidden="true"></span>';

// The indicator shows offline after 3 failed attempts in a row (feature 08, behaviour rules).
const OFFLINE_AFTER_FAILURES = 3;
// With no job and no user action, only a probe request shows that the server is back.
const PROBE_INTERVAL_MS = 4000;
// A running job makes these settings read-only (feature 08, downloading state). setJobLocked handles the map source picker.
// The area tools are in the list, so a running job keeps the area list that it started with.
const JOB_SETTINGS = ['minZoom', 'maxZoom', 'customName', 'maxConcurrent', 'failureTolerance', 'ignoreErrors', 'verifyTLS', 'ifExists',
    'outputFormat', 'dataPackage', 'includeSource', 'packageName',
    'drawRectangle', 'drawPolygon', 'drawRoute', 'importFile', 'routeBuffer', 'clearSelection'];
// The file extension of each output format is the same as output.Extension (FR-output-formats-2).
const FORMAT_EXTENSIONS = { mbtiles: '.mbtiles', osmdroid: '.sqlite', gpkg: '.gpkg' };
// The output file list shows these names for the output_format of GET /api/files.
const FORMAT_NAMES = { mbtiles: 'MBTiles', osmdroid: 'OSMDroid', gpkg: 'GeoPackage' };
// GET /api/files gives a data package this format, and a data package has no tiles to show.
const DATA_PACKAGE = 'data_package';
// The rule of datapackage.ValidName lets the web UI reject a package name before the server does.
const PACKAGE_NAME = /^[A-Za-z0-9 _-]{1,64}$/;
// The server accepts at most 20 areas in one job (FR-area-selection-5).
const MAX_AREAS = 20;
const KIND_LABEL = { bounds: 'Rectangle', polygon: 'Polygon', route_buffer: 'Route' };
// DRAW_TOOLS maps each Geoman draw shape to its toolbar button and to the hint that the draw state shows.
const DRAW_TOOLS = {
    Rectangle: ['drawRectangle', 'Drag on the map to draw a rectangle. Press Esc to cancel.'],
    Polygon: ['drawPolygon', 'Click on the map for each corner, and click the first corner to end. Press Esc to cancel.'],
    Line: ['drawRoute', 'Click on the map for each point of the route, and click the last point again to end. Press Esc to cancel.']
};
const CONNECTION_LOOK = {
    connected: ['ok', 'Connected'],
    reconnecting: ['warn', 'Reconnecting'],
    offline: ['err', 'Offline']
};

class ATAKDownloader {
    constructor() {
        this.map = null;
        // areas is the area list in job order. Each area holds its geometry as GeoJSON and its Leaflet layer.
        this.areas = [];
        this.nextAreaId = 1;
        this.selectedAreaId = null;
        // uniqueTiles is the unique tile count of the area list from the last estimate, and null before it.
        this.uniqueTiles = null;
        // maxTiles is the tile cap of a job from GET /api/config, and null until the config loads.
        this.maxTiles = null;
        this.shadeLayer = null;
        // drawShape is the Geoman shape of the open draw mode, and null when no draw mode is open.
        this.drawShape = null;
        // A newer request replaces an older one, so each response checks that its sequence number is the last one.
        this.countSeq = 0;
        this.shadeSeq = 0;
        this.mapSources = [];
        this.jobId = null;
        this.jobEvents = null;
        this.lastJobLog = null;
        this.isDarkMode = false;
        // themeChosen is true after the user selects a theme, so a change of the system theme no longer applies.
        this.themeChosen = false;
        // isDownloading is true from the start request until the shown job ends, so a second start cannot begin.
        this.isDownloading = false;
        this.previewLayer = null;
        this.searchMarker = null;
        // connectionState is connected, reconnecting or offline. Other parts of the web UI read it (FR-offline-web-ui-17).
        this.connectionState = 'connected';
        this.failedAttempts = 0;
        this.probeTimer = null;
        this.errorKey = null;
        this.retryRequest = null;
        // startReady is true when the job settings allow Start. The connection state can still disable Start.
        this.startReady = false;
        // exportBlocked is the message of an export setting that disables Start, or '' when the export panel allows Start.
        this.exportBlocked = '';
        // outputFiles is the last GET /api/files list, newest first, and null until the first list loads.
        this.outputFiles = null;
        // localBaseMap is the tile layer of the output file that the switcher selects, drawn over the online base map.
        this.localBaseMap = null;
        // baseMapChosen stops the offline fallback after the user or the fallback selects a base map.
        this.baseMapChosen = false;
        this.onlineTileLoaded = false;
        this.onlineTileFailed = false;

        this.init();
    }
    
    init() {
        this.initTheme();
        this.initMap();
        this.loadMapSources();
        this.loadSkippedSources();
        this.loadConfig();
        this.loadOutputFiles().then(() => this.fallBackToLocalBaseMap());
        this.bindEvents();
        this.updateExportPanel();
        this.logStatus('Application initialized');
    }
    
    // initTheme applies the stored theme, or the system theme until the user selects one (FR-offline-web-ui-29).
    initTheme() {
        const system = window.matchMedia('(prefers-color-scheme: dark)');
        let saved = null;
        try {
            saved = localStorage.getItem('theme');
        } catch {
            // The browser blocks storage, so the theme follows the system.
        }
        this.themeChosen = saved === 'dark' || saved === 'light';
        this.isDarkMode = this.themeChosen ? saved === 'dark' : system.matches;
        this.applyTheme();
        system.addEventListener('change', (e) => {
            if (this.themeChosen) return;
            this.isDarkMode = e.matches;
            this.applyTheme();
        });
    }
    
    toggleTheme() {
        this.isDarkMode = !this.isDarkMode;
        this.themeChosen = true;
        this.applyTheme();
        try {
            localStorage.setItem('theme', this.isDarkMode ? 'dark' : 'light');
        } catch (error) {
            this.logStatus('The browser did not store the theme, so it applies to this page only: ' + error.message, 'warning');
        }
    }
    
    // The CSS shows the moon or the sun icon from data-theme. aria-pressed tells a screen reader the state of the toggle.
    applyTheme() {
        document.documentElement.setAttribute('data-theme', this.isDarkMode ? 'dark' : 'light');
        document.getElementById('themeToggle').setAttribute('aria-pressed', String(this.isDarkMode));
    }

    initMap() {
        this.map = L.map('map').setView([39.8283, -98.5795], 4);
        const online = L.tileLayer('https://{s}.tile.openstreetmap.org/{z}/{x}/{y}.png', {
            attribution: '© OpenStreetMap contributors',
            maxZoom: 19
        }).addTo(this.map);
        // A failed OpenStreetMap tile before any OpenStreetMap tile loads means that the browser has no internet at load time (Flow 1).
        online.once('tileload', () => { this.onlineTileLoaded = true; });
        online.once('tileerror', () => {
            if (this.onlineTileLoaded) return;
            this.onlineTileFailed = true;
            this.fallBackToLocalBaseMap();
        });

        // A Leaflet control keeps the switcher on the map and stops its clicks from reaching the map.
        const switcher = document.getElementById('baseMap');
        L.DomEvent.disableClickPropagation(switcher);
        L.DomEvent.disableScrollPropagation(switcher);
        const control = L.control({ position: 'topright' });
        control.onAdd = () => switcher;
        control.addTo(this.map);
        switcher.hidden = false;

        this.initDrawControls();
        this.logStatus('Map initialized');
    }
    
    // Geoman draws, edits and removes the areas (FR-offline-web-ui-6, FR-area-selection-19).
    // The sidebar toolbar opens the draw modes, so the Geoman toolbar holds only edit, drag and removal.
    initDrawControls() {
        this.map.pm.setGlobalOptions({
            pathOptions: { color: '#3498db', weight: 2, fillOpacity: 0.2 },
            exitModeOnEscape: true
        });
        this.map.pm.addControls({
            position: 'topright',
            drawMarker: false,
            drawCircleMarker: false,
            drawPolyline: false,
            drawRectangle: false,
            drawPolygon: false,
            drawCircle: false,
            drawText: false,
            editMode: true,
            dragMode: true,
            cutPolygon: false,
            removalMode: true,
            rotateMode: false
        });
        // Geoman puts each title on the button container, so the link inside gets it as its accessible name.
        this.map.getContainer().querySelectorAll('.button-container[title] > .leaflet-buttons-control-button').forEach(button => {
            button.setAttribute('aria-label', button.parentElement.title);
        });

        this.map.on('pm:drawstart', (e) => this.setDrawState(e.shape));
        this.map.on('pm:drawend', () => this.setDrawState(null));
        this.map.on('pm:create', (e) => this.onDrawCreate(e));
        this.map.on('pm:remove', (e) => {
            const area = this.areas.find(a => a.layer === e.layer);
            if (area) this.removeArea(area.id);
        });
    }

    // startDraw opens the Geoman draw mode of shape, or closes it when it is open already.
    startDraw(shape) {
        if (this.isDownloading) return;
        if (this.map.pm.globalDrawModeEnabled() && this.drawShape === shape) {
            this.map.pm.disableDraw();
            return;
        }
        this.map.pm.enableDraw(shape);
    }

    // setDrawState marks the toolbar button of the open draw mode and shows how to cancel it. shape is null when no mode is open.
    setDrawState(shape) {
        this.drawShape = shape;
        Object.entries(DRAW_TOOLS).forEach(([name, [id]]) => {
            document.getElementById(id).setAttribute('aria-pressed', String(name === shape));
        });
        const hint = document.getElementById('drawHint');
        hint.hidden = !DRAW_TOOLS[shape];
        hint.textContent = DRAW_TOOLS[shape] ? DRAW_TOOLS[shape][1] : '';
    }

    // onDrawCreate adds the shape that the user drew to the area list.
    onDrawCreate(e) {
        const kind = { Rectangle: 'bounds', Polygon: 'polygon', Line: 'route_buffer' }[e.shape];
        if (!kind) return;
        const added = this.addArea({
            kind: kind,
            geometry: e.layer.toGeoJSON().geometry,
            bufferM: kind === 'route_buffer' ? this.routeBufferM() : null,
            name: KIND_LABEL[kind] + ' ' + this.nextAreaId
        }, e.layer);
        if (added) this.areasChanged();
    }

    // recordSuccess marks the server as reachable. Any response counts, an error status too.
    recordSuccess() {
        this.failedAttempts = 0;
        if (this.probeTimer) {
            clearInterval(this.probeTimer);
            this.probeTimer = null;
        }
        this.setConnection('connected');
    }

    // recordFailure counts one request or event stream attempt that did not reach the server.
    recordFailure() {
        this.failedAttempts++;
        this.setConnection(this.failedAttempts >= OFFLINE_AFTER_FAILURES ? 'offline' : 'reconnecting');
        if (!this.probeTimer) {
            this.probeTimer = setInterval(() => this.probe(), PROBE_INTERVAL_MS);
        }
    }

    async probe() {
        try {
            await fetch('/api/config');
            this.recordSuccess();
        } catch {
            this.recordFailure();
        }
    }

    setConnection(state) {
        this.connectionState = state;
        const chip = document.getElementById('connStatus');
        chip.className = 'chip ' + CONNECTION_LOOK[state][0];
        chip.textContent = CONNECTION_LOOK[state][1];
        // The offline state disables Start, and a reconnect enables it again when it was ready (feature 08, offline state).
        this.setStartEnabled(this.startReady);
    }

    // setStartEnabled is the one place that sets the disabled state of Start.
    // ready tells whether the job settings allow Start. Start stays disabled while the indicator reads offline or a job runs.
    // An export setting that the server rejects also keeps Start disabled.
    setStartEnabled(ready) {
        this.startReady = ready;
        document.getElementById('downloadBtn').disabled = !ready || this.connectionState === 'offline' || this.isDownloading ||
            Boolean(this.exportBlocked);
    }

    // updateExportPanel shows the file name preview, locks the output format of a resumed job and checks the package name.
    // The name rules are those of download.GenerateOutputFilename and datapackage.FileName, so the preview is the name that the server writes.
    updateExportPanel() {
        const safe = s => s.replace(/[^A-Za-z0-9 _-]/g, '').trim().replace(/ /g, '_');
        const customName = document.getElementById('customName').value.trim();
        const sourceName = document.getElementById('mapSourceSelect').value;
        let stem = '';
        // The name holds the rectangle and the zoom range over all areas, the same as jobOutputFile.
        if (this.areas.length > 0) {
            const zoom = '_z' + Math.min(...this.areas.map(a => a.minZoom)) + '-' + Math.max(...this.areas.map(a => a.maxZoom));
            if (customName) {
                stem = safe(customName) + zoom;
            } else if (sourceName) {
                const [west, south, east, north] = this.jobBound();
                stem = safe(sourceName) + '_' + south.toFixed(4) + '_' + west.toFixed(4) +
                    '_to_' + north.toFixed(4) + '_' + east.toFixed(4) + zoom;
            }
        }

        // The server resumes the file with the extension of the chosen output format, so a resume keeps the format of that file.
        const resume = document.getElementById('ifExists').value === 'resume';
        const existing = resume && stem && this.outputFiles
            ? this.outputFiles.find(f => FORMAT_EXTENSIONS[f.output_format] && f.name === stem + FORMAT_EXTENSIONS[f.output_format])
            : undefined;
        Object.keys(FORMAT_EXTENSIONS).forEach(format => {
            const radio = document.getElementById('format-' + format);
            if (existing) radio.checked = format === existing.output_format;
            radio.disabled = Boolean(existing) && format !== existing.output_format;
        });
        const lock = document.getElementById('formatLock');
        lock.hidden = !existing;
        lock.textContent = existing ? 'Resume keeps the output format of ' + existing.name + '.' : '';
        const format = Object.keys(FORMAT_EXTENSIONS).find(f => document.getElementById('format-' + f).checked);

        const dataPackage = document.getElementById('dataPackage').checked;
        const packageName = document.getElementById('packageName').value.trim();
        document.getElementById('packageOptions').disabled = !dataPackage;
        this.exportBlocked = '';
        if (dataPackage && !packageName) {
            this.exportBlocked = 'Enter a package name';
        } else if (dataPackage && !PACKAGE_NAME.test(packageName)) {
            this.exportBlocked = 'Use 1 to 64 letters, digits, spaces, _ or -';
        }
        const message = document.getElementById('packageNameMessage');
        message.hidden = !this.exportBlocked;
        message.textContent = this.exportBlocked;

        const file = stem ? stem + FORMAT_EXTENSIONS[format] : '';
        let preview = file || 'Select an area and a map source';
        if (dataPackage) preview = this.exportBlocked ? '-' : packageName.replace(/ /g, '_') + '.zip';
        document.getElementById('exportPreview').textContent = preview;
        const inner = document.getElementById('exportInner');
        inner.hidden = !(dataPackage && file && !this.exportBlocked);
        inner.textContent = inner.hidden ? '' : 'The zip holds ' + file + '.';
        this.setStartEnabled(this.startReady);
    }

    // jobBound returns [west, south, east, north] of all areas with the rule of tilecalc.JobBound.
    // The rectangle of a route_buffer area includes its buffer, with the longitude scale of its latitude nearest a pole.
    jobBound() {
        // The value is tilecalc.metersPerDegree, so the preview rounds to the digits of the server name.
        const metersPerDegree = 2 * Math.PI * 6378137 / 360;
        let out = null;
        this.areas.forEach(area => {
            const points = [area.geometry.coordinates].flat(Infinity);
            const lons = points.filter((_, i) => i % 2 === 0);
            const lats = points.filter((_, i) => i % 2 === 1);
            let b = [Math.min(...lons), Math.min(...lats), Math.max(...lons), Math.max(...lats)];
            if (area.kind === 'route_buffer') {
                const dLat = area.bufferM / metersPerDegree;
                const lat = Math.min(85.0511, Math.max(Math.abs(b[1]), Math.abs(b[3])) + dLat);
                const dLon = dLat / Math.cos(lat * Math.PI / 180);
                b = [Math.max(-180, b[0] - dLon), Math.max(-90, b[1] - dLat), Math.min(180, b[2] + dLon), Math.min(90, b[3] + dLat)];
            }
            out = out ? [Math.min(out[0], b[0]), Math.min(out[1], b[1]), Math.max(out[2], b[2]), Math.max(out[3], b[3])] : b;
        });
        return out;
    }

    // setJobLocked makes Estimate, Start and the job settings read-only while locked is true.
    setJobLocked(locked) {
        this.isDownloading = locked;
        document.getElementById('estimateBtn').disabled = locked || this.areas.length === 0;
        // An empty source list keeps the map source picker disabled after the job too.
        document.getElementById('mapSourceSelect').disabled = locked || this.mapSources.length === 0;
        JOB_SETTINGS.forEach(id => { document.getElementById(id).disabled = locked; });
        // A Geoman mode can change an area on the map, so a running job closes the modes and hides their toolbar.
        if (locked) {
            this.map.pm.disableDraw();
            this.map.pm.disableGlobalEditMode();
            this.map.pm.disableGlobalDragMode();
            this.map.pm.disableGlobalRemovalMode();
        }
        if (this.map.pm.controlsVisible() === locked) this.map.pm.toggleControls();
        this.renderAreaList();
        this.setStartEnabled(this.startReady);
    }

    // setBaseMap draws the tiles of the output file name over the online base map. An empty name shows the online base map only.
    setBaseMap(name) {
        if (this.localBaseMap) {
            this.map.removeLayer(this.localBaseMap);
            this.localBaseMap = null;
        }
        const file = name && this.outputFiles && this.outputFiles.find(f => f.name === name);
        if (!file) return;
        const options = { maxZoom: 19 };
        // Above the highest zoom level of the file, Leaflet scales its tiles up instead of requesting missing ones.
        if (file.max_zoom > 0) options.maxNativeZoom = file.max_zoom;
        // Bounds is [west, south, east, north], and Leaflet requests no tile outside it.
        if (file.bounds) options.bounds = L.latLngBounds([file.bounds[1], file.bounds[0]], [file.bounds[3], file.bounds[2]]);
        this.localBaseMap = L.tileLayer('/api/files/' + encodeURIComponent(name) + '/tiles/{z}/{x}/{y}', options).addTo(this.map);
    }

    // renderBaseMaps puts one switcher entry per output file after the online base map (FR-offline-web-ui-15).
    // The tile route cannot read a zip, so a data package gets no entry (feature 08, edge cases).
    renderBaseMaps(files) {
        const select = document.getElementById('baseMapSelect');
        const selected = select.value;
        select.replaceChildren(select.options[0]);
        files = files.filter(f => f.format !== DATA_PACKAGE);
        files.forEach(file => {
            const option = document.createElement('option');
            option.value = file.name;
            option.textContent = file.name;
            select.appendChild(option);
        });
        // A refresh can remove the selected file, and then the switcher goes back to the online base map.
        if (!files.some(f => f.name === selected)) {
            select.value = '';
            this.setBaseMap('');
        }
    }

    // fallBackToLocalBaseMap selects the most recent output file when the web UI loads with no internet (FR-offline-web-ui-16).
    // Two signals mean no internet: navigator.onLine and a failed first OpenStreetMap tile.
    // The offline indicator is not a signal, because the local tiles come from the same server that it reports on.
    // It selects a base map once, and never after the user selects one.
    fallBackToLocalBaseMap() {
        const offline = navigator.onLine === false || this.onlineTileFailed;
        if (this.baseMapChosen || !offline || this.outputFiles === null) return;
        this.baseMapChosen = true;
        // The list is newest first, and a data package has no tiles to show.
        const newest = this.outputFiles.find(f => f.format !== DATA_PACKAGE);
        if (!newest) {
            this.showError('baseMap', 'Offline: no local base map', () => {
                this.baseMapChosen = false;
                this.loadOutputFiles().then(() => this.fallBackToLocalBaseMap());
            });
            return;
        }
        document.getElementById('baseMapSelect').value = newest.name;
        this.setBaseMap(newest.name);
        this.logStatus('Offline: the base map is the output file ' + newest.name, 'warning');
    }

    // requestJSON sends a request and returns the JSON body of a 2xx response.
    // For an error status, it throws an Error with the server's error.message, and its code property holds error.code.
    // If the request does not reach the server, it throws "Server unreachable".
    async requestJSON(path, options) {
        let response;
        try {
            response = await fetch(path, options);
        } catch {
            this.recordFailure();
            throw new Error('Server unreachable');
        }
        this.recordSuccess();
        // A proxy can send an error page that is not JSON, so a parse failure keeps the status as the message.
        const body = await response.json().catch(() => null);
        if (!response.ok) {
            const error = new Error((body && body.error && body.error.message) || 'The server returned status ' + response.status);
            // The code lets a caller tell an expected refusal, such as too_many_tiles, from a failure.
            error.code = body && body.error && body.error.code;
            throw error;
        }
        return body;
    }

    postJSON(path, body) {
        return this.requestJSON(path, {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify(body)
        });
    }

    // showError shows the error banner for the request named by key (FR-offline-web-ui-19).
    // Retry calls retry, which sends the failed request again (FR-offline-web-ui-20).
    showError(key, message, retry) {
        this.errorKey = key;
        this.retryRequest = retry;
        document.getElementById('errorMessage').textContent = message;
        // An import warning has no request to send again, so its banner shows no Retry.
        document.getElementById('errorRetry').hidden = !retry;
        document.getElementById('errorBanner').hidden = false;
    }

    // hideError hides the banner only when it shows the error of key, so a success elsewhere keeps an unrelated error visible.
    hideError(key) {
        if (this.errorKey !== key) return;
        this.errorKey = null;
        this.retryRequest = null;
        document.getElementById('errorBanner').hidden = true;
    }

    retry() {
        const retry = this.retryRequest;
        this.hideError(this.errorKey);
        if (retry) retry();
    }

    // setBusy shows a spinner in a button while its request runs (FR-offline-web-ui-18).
    setBusy(button, busy) {
        button.disabled = busy;
        const spinner = button.querySelector('.spinner');
        if (busy && !spinner) button.insertAdjacentHTML('afterbegin', SPINNER);
        if (!busy && spinner) spinner.remove();
    }

    // followJob opens the SSE stream of the job and updates the UI from its events.
    followJob(id) {
        this.jobEvents = new EventSource('/api/jobs/' + encodeURIComponent(id) + '/events');
        this.jobEvents.onopen = () => this.recordSuccess();
        this.jobEvents.addEventListener('state', (e) => this.onJobState(JSON.parse(e.data)));
        this.jobEvents.addEventListener('progress', (e) => this.updateDownloadProgress(JSON.parse(e.data)));
        this.jobEvents.addEventListener('log', (e) => {
            const entry = JSON.parse(e.data);
            this.lastJobLog = entry.message;
            this.logStatus(entry.message, entry.level);
        });
        this.jobEvents.onerror = () => {
            // EventSource reconnects by itself. CLOSED means that the server refused the stream, for example after a restart lost the job.
            if (this.jobEvents && this.jobEvents.readyState === EventSource.CLOSED) {
                this.logStatus('Lost the job event stream', 'error');
                this.closeJobEvents();
                this.resetDownloadUI(id);
            } else {
                this.recordFailure();
            }
        };
    }

    // closeJobEvents closes the SSE stream, because EventSource otherwise reconnects to an ended job forever.
    closeJobEvents() {
        if (this.jobEvents) {
            this.jobEvents.close();
            this.jobEvents = null;
        }
    }

    // onJobComplete is the one refresh of the output file list after a job ends (FR-offline-web-ui-12).
    // A cancelled or failed job keeps its output file too, so every final state calls it.
    onJobComplete() {
        this.loadOutputFiles();
    }

    onJobState(record) {
        if (record.state !== 'running') {
            this.onJobComplete();
        }
        switch (record.state) {
            case 'running':
                this.setJobLocked(true);
                this.showCancelButton(true);
                // The refresh marks the output file of the job in_progress, so its Download button is disabled.
                this.loadOutputFiles();
                // A client that connects late gets the counts from the state event before the next progress event.
                this.updateDownloadProgress(record);
                break;
            case 'complete':
                this.closeJobEvents();
                this.logStatus('Download complete. Saved to ' + record.output_file, 'success');
                this.showDownloadComplete(record);
                this.resetDownloadUI(record.id);
                break;
            case 'failed':
                this.closeJobEvents();
                // A failed job sends its error as a log event first, and a client that connects late gets only this state.
                if (record.error !== this.lastJobLog) {
                    this.logStatus('Download failed: ' + record.error, 'error');
                }
                this.hideDownloadResults();
                this.resetDownloadUI(record.id);
                break;
            case 'cancelled':
                this.closeJobEvents();
                this.logStatus('Download cancelled', 'warning');
                this.resetDownloadUI(record.id);
                break;
        }
    }

    async loadMapSources() {
        const select = document.getElementById('mapSourceSelect');
        const loading = document.getElementById('sourceLoading');
        select.disabled = true;
        loading.hidden = false;
        try {
            this.mapSources = await this.requestJSON('/api/sources');
            this.populateSourceSelect();
            this.hideError('sources');
            this.logStatus('Loaded ' + this.mapSources.length + ' map sources');
        } catch (error) {
            this.logStatus('Error loading sources: ' + error.message, 'error');
            this.showError('sources', 'Map sources did not load: ' + error.message, () => this.loadMapSources());
        } finally {
            select.disabled = this.mapSources.length === 0 || this.isDownloading;
            loading.hidden = true;
        }
    }

    // loadSkippedSources fills the "N sources skipped" link under the source picker (FR-source-sync-30).
    async loadSkippedSources() {
        try {
            const response = await fetch('/api/sources/skipped');
            const skipped = await response.json();
            if (!response.ok) {
                throw new Error((skipped.error && skipped.error.message) || 'Failed to load the skipped list');
            }
            const block = document.getElementById('skippedSources');
            const list = document.getElementById('skippedList');
            const link = document.getElementById('skippedLink');
            // A path comes from the file system, so each value goes in with textContent.
            list.replaceChildren(...skipped.map(s => {
                const item = document.createElement('li');
                const path = document.createElement('code');
                path.textContent = s.path;
                item.append(path, document.createTextNode(` ${s.reason}: ${s.detail}`));
                return item;
            }));
            link.textContent = skipped.length === 1 ? '1 source skipped' : `${skipped.length} sources skipped`;
            link.onclick = (e) => {
                e.preventDefault();
                document.getElementById('skippedDialog').showModal();
            };
            block.hidden = skipped.length === 0;
        } catch (error) {
            this.logStatus('Error loading skipped sources: ' + error.message, 'error');
        }
    }

    populateSourceSelect() {
        const select = document.getElementById('mapSourceSelect');
        select.innerHTML = '<option value="">Select a map source...</option>';
        
        this.mapSources.forEach(source => {
            const option = document.createElement('option');
            option.value = source.name;
            // A native option holds text only, so the policy class badge is a text suffix.
            option.textContent = `${source.name} (${source.format}) - Zoom ${source.min_zoom}-${source.max_zoom} [${source.policy_class}]`;
            option.title = `${source.name} - Format: ${source.format} - Zoom range: ${source.min_zoom} to ${source.max_zoom} - Policy: ${source.policy_class}`;
            option.className = 'policy-' + source.policy_class;
            select.appendChild(option);
        });
    }
    
    bindEvents() {
        document.getElementById('themeToggle').addEventListener('click', () => this.toggleTheme());
        document.getElementById('errorRetry').addEventListener('click', () => this.retry());
        document.getElementById('drawRectangle').addEventListener('click', () => this.startDraw('Rectangle'));
        document.getElementById('drawPolygon').addEventListener('click', () => this.startDraw('Polygon'));
        document.getElementById('drawRoute').addEventListener('click', () => this.startDraw('Line'));
        document.getElementById('importFile').addEventListener('change', (e) => {
            const file = e.target.files[0];
            // A cleared value lets the user pick the same file again after a fix.
            e.target.value = '';
            if (file) this.importAreaFile(file);
        });
        [document.getElementById('importDrop'), this.map.getContainer()].forEach(zone => {
            zone.addEventListener('dragover', (e) => {
                e.preventDefault();
                document.getElementById('importDrop').classList.add('over');
            });
            zone.addEventListener('dragleave', () => document.getElementById('importDrop').classList.remove('over'));
            zone.addEventListener('drop', (e) => this.onAreaDrop(e));
        });
        document.getElementById('clearSelection').addEventListener('click', () => this.clearSelection());
        document.getElementById('estimateBtn').addEventListener('click', () => this.estimateDownload());
        document.getElementById('previewBtn').addEventListener('click', () => this.previewTiles());
        document.getElementById('downloadBtn').addEventListener('click', () => this.startDownload());
        document.getElementById('cancelBtn').addEventListener('click', () => this.cancelDownload());
        document.getElementById('searchBtn').addEventListener('click', () => this.searchAddress());
        document.getElementById('addressSearch').addEventListener('keypress', (e) => {
            if (e.key === 'Enter') this.searchAddress();
        });
        document.getElementById('mapSourceSelect').addEventListener('change', (e) => this.onSourceChange(e.target.value));
        document.getElementById('baseMapSelect').addEventListener('change', (e) => {
            this.baseMapChosen = true;
            this.hideError('baseMap');
            this.setBaseMap(e.target.value);
        });
        document.getElementById('minZoom').addEventListener('change', () => this.validateZoomLevels());
        document.getElementById('maxZoom').addEventListener('change', () => this.validateZoomLevels());
        // The file name preview reads each of these settings. These listeners come after the zoom check, so they see its result.
        ['customName', 'minZoom', 'maxZoom', 'mapSourceSelect', 'ifExists', 'dataPackage', 'packageName',
            ...Object.keys(FORMAT_EXTENSIONS).map(f => 'format-' + f)].forEach(id => {
            const control = document.getElementById(id);
            control.addEventListener('input', () => this.updateExportPanel());
            control.addEventListener('change', () => this.updateExportPanel());
        });
    }
    
    // clearSelection removes every area from the map and the area list.
    clearSelection() {
        if (this.isDownloading) return;
        this.areas.forEach(area => this.map.removeLayer(area.layer));
        this.areas = [];
        this.selectedAreaId = null;
        this.clearPreview();
        this.areasChanged();
        this.logStatus('Selection cleared');
    }

    // routeBufferM returns the route buffer of the sidebar in meters. An empty field gives the default 500 (FR-area-selection-20).
    routeBufferM() {
        return parseFloat(document.getElementById('routeBuffer').value) || 500;
    }

    // addArea puts an area at the end of the list and selects it. It returns false when the list holds MAX_AREAS areas.
    // spec holds kind, geometry, name and bufferM, and minZoom and maxZoom when the area has its own zoom range.
    // layer is the drawn layer. With no layer, addArea draws the geometry. The caller calls areasChanged.
    addArea(spec, layer) {
        if (this.areas.length >= MAX_AREAS) {
            if (layer) this.map.removeLayer(layer);
            this.showError('areaLimit', 'A job holds at most ' + MAX_AREAS + ' areas. Remove an area first.', null);
            return false;
        }
        // L.geoJSON returns a group, and Geoman edits and removes the layer inside it, so the area keeps that layer.
        const drawn = layer || L.geoJSON(spec.geometry, { style: { color: '#3498db', weight: 2, fillOpacity: 0.2 } }).getLayers()[0];
        const area = {
            id: this.nextAreaId++,
            kind: spec.kind,
            name: spec.name,
            geometry: spec.geometry,
            bufferM: spec.bufferM,
            minZoom: spec.minZoom ?? parseInt(document.getElementById('minZoom').value),
            maxZoom: spec.maxZoom ?? parseInt(document.getElementById('maxZoom').value),
            tiles: null,
            layer: drawn.addTo(this.map)
        };
        // Edit mode and drag mode move the area, so the geometry follows each change.
        const follow = () => {
            area.geometry = area.layer.toGeoJSON().geometry;
            this.areasChanged();
        };
        area.layer.on('pm:edit', follow);
        area.layer.on('pm:dragend', follow);
        area.layer.on('click', () => this.selectArea(area.id));
        this.areas.push(area);
        this.selectedAreaId = area.id;
        return true;
    }

    removeArea(id) {
        const area = this.areas.find(a => a.id === id);
        if (!area || this.isDownloading) return;
        this.map.removeLayer(area.layer);
        this.areas = this.areas.filter(a => a !== area);
        this.hideError('areaLimit');
        if (this.selectedAreaId === id) {
            this.selectedAreaId = this.areas.length > 0 ? this.areas[this.areas.length - 1].id : null;
        }
        this.areasChanged();
    }

    selectArea(id) {
        this.selectedAreaId = id;
        this.renderAreaList();
        return this.shadeSelectedArea();
    }

    // setAreaZoom sets the min or max zoom of an area from its row (FR-area-selection-23). field is minZoom or maxZoom.
    // An inverted range moves the other end, as validateZoomLevels does for the sidebar range.
    setAreaZoom(id, field, value) {
        const area = this.areas.find(a => a.id === id);
        const zoom = parseInt(value);
        if (!area || Number.isNaN(zoom)) {
            this.renderAreaList();
            return;
        }
        area[field] = Math.min(25, Math.max(0, zoom));
        if (area.minZoom > area.maxZoom) {
            if (field === 'minZoom') area.maxZoom = area.minZoom; else area.minZoom = area.maxZoom;
        }
        this.areasChanged();
    }

    // areasChanged redraws the area list and asks the server for the new counts and shading.
    // The old estimate no longer matches the list, so Start waits for a new estimate.
    areasChanged() {
        this.uniqueTiles = null;
        this.areas.forEach(a => { a.tiles = null; });
        this.renderAreaList();
        // The areas are part of the generated file name.
        this.updateExportPanel();
        if (this.areas.length > 0) this.enableEstimation(); else this.disableEstimation();
        return Promise.all([this.refreshCounts(), this.shadeSelectedArea()]);
    }

    // areaRequest returns one area in the job API shape (FR-area-selection-25).
    areaRequest(area) {
        const request = { kind: area.kind, geometry: area.geometry, min_zoom: area.minZoom, max_zoom: area.maxZoom, name: area.name };
        if (area.kind === 'route_buffer') request.buffer_m = area.bufferM;
        return request;
    }

    areaPayload() {
        return this.areas.map(area => this.areaRequest(area));
    }

    // applyCounts copies the counts of an estimate to the areas that the request sent, in request order.
    applyCounts(areas, result) {
        areas.forEach((area, i) => { area.tiles = result.areas[i].tiles; });
        this.uniqueTiles = result.unique_tiles;
        this.renderAreaList();
    }

    // refreshCounts asks POST /api/estimate for the tile count of each area and the unique total.
    async refreshCounts() {
        const seq = ++this.countSeq;
        if (this.areas.length === 0) {
            this.hideError('areas');
            return;
        }
        const areas = this.areas.slice();
        try {
            const result = await this.postJSON('/api/estimate', { areas: this.areaPayload() });
            if (seq !== this.countSeq) return;
            this.applyCounts(areas, result);
            this.hideError('areas');
        } catch (error) {
            if (seq !== this.countSeq) return;
            this.showError('areas', 'The area list is not valid: ' + error.message, () => this.refreshCounts());
        }
    }

    // renderAreaList shows one row per area with its kind, name, zoom range, tile count and Remove (FR-area-selection-22).
    // Area names come from the user or from a file, so they go in through textContent only.
    renderAreaList() {
        const list = document.getElementById('areaList');
        list.replaceChildren();
        document.getElementById('areaEmpty').hidden = this.areas.length > 0;
        this.areas.forEach(area => {
            const row = document.createElement('li');
            row.classList.toggle('selected', area.id === this.selectedAreaId);
            const top = document.createElement('div');
            top.className = 'area-top';
            // The name is a button, so the keyboard can select the area that the map shades.
            const name = document.createElement('button');
            name.type = 'button';
            name.className = 'area-name';
            name.textContent = area.name;
            name.setAttribute('aria-pressed', String(area.id === this.selectedAreaId));
            name.addEventListener('click', () => this.selectArea(area.id));
            const remove = document.createElement('button');
            remove.type = 'button';
            remove.className = 'btn btn-sm';
            remove.textContent = 'Remove';
            remove.setAttribute('aria-label', 'Remove ' + area.name);
            remove.disabled = this.isDownloading;
            remove.addEventListener('click', () => this.removeArea(area.id));
            top.append(name, remove);

            const minInput = document.createElement('input');
            const maxInput = document.createElement('input');
            [[minInput, 'minZoom'], [maxInput, 'maxZoom']].forEach(([input, field]) => {
                input.type = 'number';
                input.min = '0';
                input.max = '25';
                input.value = String(area[field]);
                input.disabled = this.isDownloading;
                input.addEventListener('change', (e) => this.setAreaZoom(area.id, field, e.target.value));
            });
            minInput.setAttribute('aria-label', 'Min zoom of ' + area.name);
            maxInput.setAttribute('aria-label', 'Max zoom of ' + area.name);
            const zoom = document.createElement('div');
            zoom.className = 'area-zoom';
            zoom.append('Zoom', minInput, '–', maxInput);

            const count = document.createElement('div');
            count.className = 'area-count';
            const kind = KIND_LABEL[area.kind] + (area.kind === 'route_buffer' ? ' + ' + area.bufferM + ' m buffer' : '');
            count.textContent = kind + ' · ' + (area.tiles === null ? 'counting tiles' : area.tiles.toLocaleString() + ' tiles');
            row.append(top, zoom, count);
            list.appendChild(row);
        });
        this.renderAreaTotal();
    }

    // renderAreaTotal shows the unique tile count under the list, in red with a banner above the tile cap.
    renderAreaTotal() {
        const total = document.getElementById('areaTotal');
        const over = this.maxTiles !== null && this.uniqueTiles !== null && this.uniqueTiles > this.maxTiles;
        total.hidden = this.areas.length === 0 || this.uniqueTiles === null;
        total.classList.toggle('over', over);
        total.textContent = this.uniqueTiles === null ? '' : 'Total: ' + this.uniqueTiles.toLocaleString() + ' tiles (overlaps counted once)';
        if (over) {
            this.showError('cap', 'The areas hold ' + this.uniqueTiles.toLocaleString() + ' tiles, and the tile cap is ' +
                this.maxTiles.toLocaleString() + '. Remove an area or lower a zoom range.', null);
        } else {
            this.hideError('cap');
        }
    }

    // shadeSelectedArea draws the tiles of the selected area at its max zoom (FR-area-selection-24).
    // At 5,000 tiles or more the route returns too_many_tiles, and the map then shows the outline only.
    async shadeSelectedArea() {
        const seq = ++this.shadeSeq;
        if (this.shadeLayer) {
            this.map.removeLayer(this.shadeLayer);
            this.shadeLayer = null;
        }
        const area = this.areas.find(a => a.id === this.selectedAreaId);
        if (!area) return;
        try {
            const result = await this.postJSON('/api/areas/tiles', { area: this.areaRequest(area), zoom: area.maxZoom });
            if (seq !== this.shadeSeq) return;
            const squares = result.tiles.map(([x, y]) => L.rectangle(this.tileBounds(x, y, area.maxZoom), {
                pmIgnore: true, interactive: false, weight: 1, color: '#2f6b4f', fillOpacity: 0.25
            }));
            this.shadeLayer = L.layerGroup(squares).addTo(this.map);
        } catch (error) {
            if (seq !== this.shadeSeq) return;
            if (error.code === 'too_many_tiles') {
                this.logStatus(area.name + ' selects 5,000 tiles or more at zoom ' + area.maxZoom + ', so the map shows its outline only');
            } else {
                this.logStatus('The tiles of ' + area.name + ' did not load: ' + error.message, 'error');
            }
        }
    }

    // tileBounds returns the corners of the Web Mercator tile x, y at zoom level z as Leaflet [lat, lon] pairs.
    tileBounds(x, y, z) {
        const n = Math.pow(2, z);
        const lat = (row) => Math.atan(Math.sinh(Math.PI * (1 - 2 * row / n))) * 180 / Math.PI;
        return [[lat(y + 1), x / n * 360 - 180], [lat(y), (x + 1) / n * 360 - 180]];
    }

    // onAreaDrop imports the first file of a drop on the drop zone or on the map (FR-area-selection-21).
    onAreaDrop(e) {
        e.preventDefault();
        document.getElementById('importDrop').classList.remove('over');
        if (this.isDownloading) return Promise.resolve();
        const file = e.dataTransfer.files[0];
        return file ? this.importAreaFile(file) : Promise.resolve();
    }

    // importAreaFile sends a GeoJSON, KML or KMZ file to POST /api/areas/parse, which holds the one area file parser (#53).
    // The query sends the sidebar defaults, because the server uses 0 for a value that the file and the query do not set.
    async importAreaFile(file) {
        if (this.isDownloading) return;
        const form = new FormData();
        form.append('file', file, file.name);
        const query = new URLSearchParams({
            min_zoom: document.getElementById('minZoom').value,
            max_zoom: document.getElementById('maxZoom').value,
            buffer_m: String(this.routeBufferM())
        });
        let result;
        try {
            result = await this.requestJSON('/api/areas/parse?' + query, { method: 'POST', body: form });
        } catch (error) {
            this.logStatus('Import error: ' + error.message, 'error');
            this.showError('import', 'Import of ' + file.name + ' failed: ' + error.message, () => this.importAreaFile(file));
            return;
        }
        this.hideError('import');
        let added = 0;
        for (const a of result.areas) {
            const ok = this.addArea({
                kind: a.kind, geometry: a.geometry, bufferM: a.buffer_m, minZoom: a.min_zoom, maxZoom: a.max_zoom,
                name: a.name || file.name + ' ' + (added + 1)
            });
            if (!ok) break;
            added++;
        }
        this.logStatus('Imported ' + added + ' areas from ' + file.name, 'success');
        if (result.warnings.length > 0) {
            this.showError('import', file.name + ': ' + result.warnings.join('. '), null);
        }
        if (added > 0) await this.areasChanged();
    }

    enableEstimation() {
        document.getElementById('estimateBtn').disabled = this.isDownloading;
        document.getElementById('previewBtn').disabled = false;
        this.setStartEnabled(false);
    }
    
    disableEstimation() {
        document.getElementById('estimateBtn').disabled = true;
        document.getElementById('previewBtn').disabled = true;
        this.setStartEnabled(false);
        document.getElementById('estimationResult').innerHTML = '';
        document.getElementById('estimationResult').className = '';
        this.clearPreview();
    }
    
    async estimateDownload() {
        if (this.isDownloading) return;
        if (this.areas.length === 0 || !document.getElementById('mapSourceSelect').value) {
            this.logStatus('Select area and map source first', 'error');
            return;
        }
        const button = document.getElementById('estimateBtn');
        const resultDiv = document.getElementById('estimationResult');
        this.setBusy(button, true);
        this.setStartEnabled(false);
        resultDiv.className = 'card';
        resultDiv.innerHTML = SPINNER;
        resultDiv.append(' Estimating the job size');
        // The estimate holds the area counts too, so it takes the place of a count that runs.
        const seq = ++this.countSeq;
        const areas = this.areas.slice();
        try {
            // The server picks the size per tile from the tile type of the map source (FR-offline-web-ui-23).
            const result = await this.postJSON('/api/estimate', {
                source: document.getElementById('mapSourceSelect').value,
                areas: this.areaPayload()
            });
            // An area change during the request starts a new count, and then this result no longer matches the list.
            if (seq !== this.countSeq) {
                resultDiv.className = '';
                resultDiv.textContent = '';
                return;
            }
            this.applyCounts(areas, result);
            this.displayEstimation(result);
            this.hideError('estimate');
            this.setStartEnabled(true);
            this.logStatus('Download estimation completed');
        } catch (error) {
            resultDiv.className = '';
            resultDiv.textContent = '';
            this.logStatus('Estimation error: ' + error.message, 'error');
            this.showError('estimate', 'Estimate failed: ' + error.message, () => this.estimateDownload());
        } finally {
            this.setBusy(button, false);
            // A job can start while the estimate runs, and then Estimate stays read-only.
            button.disabled = this.isDownloading;
        }
    }
    
    async previewTiles() {
        // The preview route takes one rectangle, so it shows the bounding rectangle of the selected area.
        const area = this.areas.find(a => a.id === this.selectedAreaId);
        if (!area || !document.getElementById('mapSourceSelect').value) {
            this.logStatus('Select area and map source first', 'error');
            return;
        }
        const button = document.getElementById('previewBtn');
        this.setBusy(button, true);
        try {
            this.clearPreview();
            this.logStatus('Loading preview tiles...', 'info');
            const bounds = area.layer.getBounds();
            const result = await this.postJSON('/api/preview', {
                source: document.getElementById('mapSourceSelect').value,
                bounds: [bounds.getWest(), bounds.getSouth(), bounds.getEast(), bounds.getNorth()],
                min_zoom: area.minZoom,
                max_zoom: area.maxZoom
            });
            this.displayPreview(result);
            this.hideError('preview');
            this.logStatus('Preview loaded: ' + result.total_tiles + ' tiles at zoom ' + result.zoom);
        } catch (error) {
            this.logStatus('Preview error: ' + error.message, 'error');
            this.showError('preview', 'Preview failed: ' + error.message, () => this.previewTiles());
        } finally {
            this.setBusy(button, false);
        }
    }
    
    displayPreview(previewData) {
        this.previewLayer = L.layerGroup().addTo(this.map);
        previewData.tiles.forEach(tile => {
            const bounds = L.latLngBounds(
                [tile.bounds[1], tile.bounds[0]],
                [tile.bounds[3], tile.bounds[2]]
            );
            // pmIgnore keeps the preview out of the Geoman edit and removal modes.
            const imageOverlay = L.imageOverlay(tile.url, bounds, { opacity: 0.8, crossOrigin: 'anonymous', pmIgnore: true });
            this.previewLayer.addLayer(imageOverlay);
        });
        const estimationResult = document.getElementById('estimationResult');
        let previewInfo = document.getElementById('previewInfo');
        if (previewInfo) previewInfo.remove();
        previewInfo = document.createElement('div');
        previewInfo.id = 'previewInfo';
        previewInfo.className = 'banner info';
        const text = document.createElement('div');
        const title = document.createElement('strong');
        title.textContent = 'Preview:';
        const zoom = document.createElement('p');
        zoom.className = 'hint';
        zoom.textContent = 'Zoom Level ' + previewData.zoom;
        const clear = document.createElement('button');
        clear.type = 'button';
        clear.className = 'btn btn-sm';
        clear.innerHTML = ICON_CANCEL + ' Clear Preview';
        clear.addEventListener('click', () => this.clearPreview());
        // The map source name comes from the server, so append() inserts it as text.
        text.append(title, ' ' + previewData.total_tiles + ' tiles from "' + previewData.source + '"', zoom, clear);
        previewInfo.appendChild(text);
        estimationResult.parentNode.insertBefore(previewInfo, estimationResult.nextSibling);
    }
    
    clearPreview() {
        if (this.previewLayer) {
            this.map.removeLayer(this.previewLayer);
            this.previewLayer = null;
        }
        const previewInfo = document.getElementById('previewInfo');
        if (previewInfo) previewInfo.remove();
        this.logStatus('Preview cleared');
    }
    
    // loadOutputFiles reads GET /api/files and shows the list. The server sorts it newest first.
    async loadOutputFiles() {
        try {
            this.renderOutputFiles(await this.requestJSON('/api/files'));
            this.hideError('files');
        } catch (error) {
            this.logStatus('Error loading output files: ' + error.message, 'error');
            this.showError('files', 'Output files did not load: ' + error.message, () => this.loadOutputFiles());
        }
    }

    // renderOutputFiles replaces the list with one row per file. Server strings go in through textContent only.
    renderOutputFiles(files) {
        this.outputFiles = files;
        this.renderBaseMaps(files);
        const list = document.getElementById('outputFileList');
        list.replaceChildren();
        document.getElementById('outputFileEmpty').hidden = files.length > 0;
        files.forEach(file => {
            const row = document.createElement('li');
            const info = document.createElement('div');
            info.className = 'file-info';
            const name = document.createElement('div');
            name.className = 'file-name';
            name.textContent = file.name;
            const details = document.createElement('div');
            details.className = 'hint';
            // A data package holds no tile metadata of its own, so its row has no tile format and no zoom range.
            const dataPackage = file.format === DATA_PACKAGE;
            details.textContent = [
                this.formatSize(file.size),
                file.source,
                dataPackage ? 'Data package' : FORMAT_NAMES[file.output_format],
                dataPackage ? '' : file.format,
                dataPackage ? '' : 'Zoom ' + file.min_zoom + '-' + file.max_zoom,
                new Date(file.modified).toLocaleString()
            ].filter(Boolean).join(' · ');
            info.append(name, details);

            const button = document.createElement('button');
            button.type = 'button';
            button.className = 'btn btn-sm';
            // ICON_DOWNLOAD is a constant with no server text, so innerHTML is safe for it.
            button.innerHTML = ICON_DOWNLOAD;
            button.append(' Download');
            // A running job still writes the file, so a download would get a part of it.
            button.disabled = file.in_progress;
            if (file.in_progress) {
                button.title = 'A job is writing this file';
            }
            // The server sends Content-Disposition: attachment, so the browser saves the file and stays on the page.
            button.addEventListener('click', () => {
                window.location.href = '/api/files/' + encodeURIComponent(file.name);
            });
            row.append(info, button);
            list.appendChild(row);
        });
        // A new file can change the format lock of a resumed job.
        this.updateExportPanel();
    }

    formatSize(bytes) {
        if (bytes >= 1024 * 1024 * 1024) return (bytes / (1024 * 1024 * 1024)).toFixed(2) + ' GB';
        if (bytes >= 1024 * 1024) return (bytes / (1024 * 1024)).toFixed(1) + ' MB';
        return Math.ceil(bytes / 1024) + ' KB';
    }

    // serve --no-geocode removes place search, so the web UI hides the search box (FR-polite-downloading-26).
    // The tile cap marks an area list that the server would reject, before the user starts the job.
    async loadConfig() {
        try {
            const config = await this.requestJSON('/api/config');
            if (config.geocode === false) {
                document.getElementById('addressSearchGroup').hidden = true;
            }
            this.maxTiles = config.max_tiles;
            this.renderAreaTotal();
        } catch (error) {
            this.logStatus('Error loading config: ' + error.message, 'error');
        }
    }

    async searchAddress() {
        const query = document.getElementById('addressSearch').value.trim();
        if (!query) {
            this.logStatus('Please enter a location to search', 'error');
            return;
        }
        try {
            this.logStatus('Searching for "' + query + '"...', 'info');
            const response = await fetch('/api/geocode', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ query: query })
            });
            const result = await response.json();
            if (response.ok && result.results && result.results.length > 0) {
                const first = result.results[0];
                this.map.setView([first.lat, first.lon], 12);
                if (this.searchMarker) this.map.removeLayer(this.searchMarker);
                // Leaflet inserts a string popup as HTML, so the place name from Nominatim goes in a DOM node as text.
                const popup = document.createElement('div');
                const name = document.createElement('strong');
                name.textContent = first.display_name;
                const position = document.createElement('small');
                position.textContent = 'Lat: ' + first.lat.toFixed(6) + ', Lon: ' + first.lon.toFixed(6);
                popup.append(name, document.createElement('br'), position);
                this.searchMarker = L.marker([first.lat, first.lon], { pmIgnore: true })
                    .addTo(this.map)
                    .bindPopup(popup)
                    .openPopup();
                this.logStatus('Found: ' + first.display_name, 'success');
            } else if (response.status === 502) {
                this.logStatus('Place search is unavailable', 'error');
            } else if (!response.ok) {
                this.logStatus((result.error && result.error.message) || 'Place search failed', 'error');
            } else {
                this.logStatus('No results found for "' + query + '"', 'warning');
            }
        } catch (error) {
            this.logStatus('Search error: ' + error.message, 'error');
        }
    }
    
    displayEstimation(result) {
        const resultDiv = document.getElementById('estimationResult');
        let className = 'ok';
        let warningText = '';
        if (result.total_tiles > 10000) {
            className = 'warn';
            warningText = '⚠️ Large download - may take significant time';
        }
        if (result.total_tiles > 100000) {
            className = 'err';
            warningText = '⚠️ Very large download! Consider reducing zoom range';
        }
        const estimatedSeconds = result.total_tiles / 10;
        const estimatedTime = this.formatTime(estimatedSeconds);
        const formattedSize = result.estimated_size_mb >= 1024 ? 
            (result.estimated_size_mb / 1024).toFixed(2) + ' GB' : result.estimated_size_mb.toFixed(2) + ' MB';
        resultDiv.className = 'card ' + className;
        resultDiv.textContent = '';
        const title = document.createElement('strong');
        title.textContent = 'Download Estimation:';
        resultDiv.append(title);
        [
            'Total Tiles: ' + result.total_tiles.toLocaleString(),
            'Estimated Size: ' + formattedSize,
            'Zoom Range: ' + result.zoom_range[0] + ' - ' + result.zoom_range[1],
            'Est. Time: ~' + estimatedTime
        ].forEach(line => resultDiv.append(document.createElement('br'), line));
        if (warningText) {
            const warning = document.createElement('small');
            warning.className = 'warn-text';
            warning.textContent = warningText;
            resultDiv.append(document.createElement('br'), warning);
        }
    }
    
    formatTime(seconds) {
        if (seconds < 60) return Math.round(seconds) + 's';
        if (seconds < 3600) return Math.round(seconds/60) + 'm';
        if (seconds < 86400) return Math.round(seconds/3600) + 'h';
        return Math.round(seconds/86400) + 'd';
    }
    
    async startDownload() {
        if (this.isDownloading) {
            this.logStatus('A job is running. Cancel it or wait for it to end', 'error');
            return;
        }
        const sourceName = document.getElementById('mapSourceSelect').value;
        // An empty select value matches no source, so this one check covers the case with no source selected.
        const source = this.mapSources.find(s => s.name === sourceName);
        if (this.areas.length === 0 || !source) {
            this.logStatus('Select area and map source first', 'error');
            return;
        }
        // Retry in the error banner calls this method with Start disabled, so the export check repeats here.
        if (this.exportBlocked) {
            this.logStatus(this.exportBlocked, 'error');
            return;
        }
        const concurrency = parseInt(document.getElementById('maxConcurrent').value) || 5;
        const request = {
            source: sourceName,
            areas: this.areaPayload(),
            name: document.getElementById('customName').value.trim(),
            concurrency: concurrency,
            failure_tolerance: parseFloat(document.getElementById('failureTolerance').value) || 0.3,
            ignore_errors: document.getElementById('ignoreErrors').checked,
            verify_tls: document.getElementById('verifyTLS').checked,
            // The server rejects an existing output file with 409 output_exists unless the user picks resume or overwrite (FR-offline-web-ui-22).
            if_exists: document.getElementById('ifExists').value,
            output_format: Object.keys(FORMAT_EXTENSIONS).find(f => document.getElementById('format-' + f).checked),
            // The server ignores include_source and package_name without data_package, so the request sends them always.
            data_package: document.getElementById('dataPackage').checked,
            include_source: document.getElementById('includeSource').checked,
            package_name: document.getElementById('packageName').value.trim()
        };
        // A restricted map source needs the policy warning dialog first (FR-polite-downloading-20, 21).
        if (source.policy_class === 'restricted') {
            const dialog = document.getElementById('policyDialog');
            const accept = document.getElementById('policyAccept');
            const start = document.getElementById('policyStart');
            const link = document.getElementById('policyLink');
            document.getElementById('policyBadge').textContent = source.policy_class;
            document.getElementById('policyTitle').textContent = source.name;
            document.getElementById('policyReason').textContent = source.policy_reason;
            document.getElementById('policyRate').textContent = source.requests_per_second + ' request/s to ' + source.policy_host;
            // A source file can hold a javascript: URL, so the link takes only an http: or https: URL.
            let href = '';
            try {
                const url = new URL(source.policy_reference_url);
                if (url.protocol === 'http:' || url.protocol === 'https:') href = url.href;
            } catch {
                // The reference URL is empty or does not parse, so the dialog shows no link.
            }
            link.hidden = !href;
            link.href = href;
            accept.checked = false;
            start.disabled = true;
            accept.onchange = () => { start.disabled = !accept.checked; };
            // Escape closes the dialog and keeps the previous return value, so the value is cleared first.
            dialog.returnValue = '';
            const accepted = await new Promise(resolve => {
                dialog.addEventListener('close', () => resolve(dialog.returnValue === 'start' && accept.checked), { once: true });
                dialog.showModal();
            });
            if (!accepted) {
                this.logStatus('Download not started: the policy warning was cancelled', 'warning');
                return;
            }
            request.accept_terms = true;
        }

        this.hideDownloadResults();
        // The lock comes before the request, so no second start or estimate begins while the request runs.
        this.setJobLocked(true);
        document.getElementById('downloadBtn').innerHTML = SPINNER + ' Downloading...';
        try {
            const result = await this.postJSON('/api/jobs', request);
            this.hideError('job');
            this.jobId = result.id;
            this.lastJobLog = null;
            this.logStatus('Download started with ' + concurrency + ' concurrent connections', 'info');
            this.logStatus('Output file: ' + result.output_file, 'info');
            this.followJob(result.id);
        } catch (error) {
            this.logStatus('Download error: ' + error.message, 'error');
            // A 409 output_exists message names if_exists, so the user can pick Resume or Overwrite and then Retry.
            this.showError('job', 'The job did not start: ' + error.message, () => this.startDownload());
            // This request got no job id, so the reset applies only when the panel shows no job.
            this.resetDownloadUI(null);
        }
    }

    // updateDownloadProgress shows the counts, the rate and the ETA of a job (FR-offline-web-ui-21).
    // data is a progress event or a job record. A job record has no rate and no ETA.
    updateDownloadProgress(data) {
        const processed = data.downloaded + data.skipped + data.failed;
        const percent = data.total > 0 ? Math.min(100, processed / data.total * 100) : 0;
        const bar = document.getElementById('jobBar');
        bar.classList.toggle('done', percent >= 100);
        bar.setAttribute('aria-valuenow', percent.toFixed(1));
        document.getElementById('jobBarFill').style.width = percent + '%';
        document.getElementById('jobSummary').textContent =
            processed.toLocaleString() + ' / ' + data.total.toLocaleString() + ' processed · ' + percent.toFixed(1) + '%';
        document.getElementById('jobDownloaded').textContent = data.downloaded.toLocaleString();
        document.getElementById('jobSkipped').textContent = data.skipped.toLocaleString();
        const failed = document.getElementById('jobFailed');
        failed.textContent = data.failed.toLocaleString();
        failed.className = data.failed > 0 ? 'warn-text' : '';
        document.getElementById('jobRate').textContent = (data.rate || 0).toFixed(1) + ' tiles/s';
        document.getElementById('jobEta').textContent = data.eta_seconds > 0 ? this.formatTime(data.eta_seconds) : '-';
        document.getElementById('downloadProgress').hidden = false;
    }

    // resetDownloadUI ends the display of the job jobId. It does nothing when the panel shows a different job.
    resetDownloadUI(jobId) {
        if (jobId !== this.jobId) return;
        this.jobId = null;
        this.setJobLocked(false);
        this.setStartEnabled(Boolean(this.areas.length > 0 && document.getElementById('mapSourceSelect').value));
        document.getElementById('downloadBtn').innerHTML = ICON_DOWNLOAD + ' Start Download';
        document.getElementById('downloadProgress').hidden = true;
        this.showCancelButton(false);
    }
    
    onSourceChange(sourceName) {
        if (sourceName) {
            const source = this.mapSources.find(s => s.name === sourceName);
            if (source) {
                document.getElementById('minZoom').min = source.min_zoom;
                document.getElementById('minZoom').max = source.max_zoom;
                document.getElementById('maxZoom').min = source.min_zoom;
                document.getElementById('maxZoom').max = source.max_zoom;
                document.getElementById('minZoom').value = Math.max(1, source.min_zoom);
                document.getElementById('maxZoom').value = Math.min(15, source.max_zoom);
                this.logStatus('Selected source: ' + sourceName);
            }
        }
    }
    
    validateZoomLevels() {
        const minZoom = parseInt(document.getElementById('minZoom').value);
        const maxZoom = parseInt(document.getElementById('maxZoom').value);
        if (minZoom > maxZoom) {
            document.getElementById('maxZoom').value = minZoom;
        }
    }
    
    logStatus(message, type) {
        const statusLog = document.getElementById('statusLog');
        const timestamp = new Date().toLocaleTimeString();
        const className = type === 'error' ? 'log-error' : type === 'success' ? 'log-success' : type === 'warning' ? 'log-warning' : '';
        const logEntry = document.createElement('div');
        logEntry.className = className;
        logEntry.textContent = '[' + timestamp + '] ' + message;
        statusLog.appendChild(logEntry);
        statusLog.scrollTop = statusLog.scrollHeight;
        while (statusLog.children.length > 50) statusLog.removeChild(statusLog.firstChild);
    }
    
    showDownloadComplete(record) {
        document.getElementById('downloadResultMessage').textContent = 'Downloaded ' + record.downloaded + ', skipped ' + record.skipped +
            ' and failed ' + record.failed + ' of ' + record.total + ' tiles.';
        document.getElementById('downloadResultFile').textContent = record.output_file;
        document.getElementById('downloadResults').style.display = 'block';
        document.getElementById('downloadResults').scrollIntoView({ behavior: 'smooth' });
    }
    
    hideDownloadResults() {
        document.getElementById('downloadResults').style.display = 'none';
    }
    
    // cancelDownload asks the server to cancel the job. The final state event resets the UI.
    async cancelDownload() {
        if (!this.isDownloading || !this.jobId) {
            return;
        }
        this.logStatus('Cancelling download...', 'warning');
        document.getElementById('cancelBtn').disabled = true;
        document.getElementById('cancelBtn').innerHTML = SPINNER + ' Cancelling...';
        try {
            await this.requestJSON('/api/jobs/' + encodeURIComponent(this.jobId), { method: 'DELETE' });
            this.hideError('cancel');
        } catch (error) {
            this.logStatus('Cancel error: ' + error.message, 'error');
            this.showError('cancel', 'The job did not cancel: ' + error.message, () => this.cancelDownload());
            this.showCancelButton(true);
        }
    }
    
    showCancelButton(show) {
        const cancelBtn = document.getElementById('cancelBtn');
        cancelBtn.style.display = show ? 'inline-flex' : 'none';
        cancelBtn.disabled = false;
        cancelBtn.innerHTML = ICON_CANCEL + ' Cancel';
    }
}

document.addEventListener('DOMContentLoaded', () => {
    window.app = new ATAKDownloader();
});
