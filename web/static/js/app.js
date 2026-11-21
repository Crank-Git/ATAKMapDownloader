// ATAK Tile Downloader web application. It runs jobs through the job API and follows them over SSE.
class ATAKDownloader {
    constructor() {
        this.map = null;
        this.drawControl = null;
        this.selectedArea = null;
        this.mapSources = [];
        this.jobId = null;
        this.jobEvents = null;
        this.lastJobLog = null;
        this.isDarkMode = false;
        this.loadingModal = null;
        this.isDownloading = false;
        this.previewLayer = null;
        this.searchMarker = null;
        
        this.init();
    }
    
    init() {
        this.initTheme();
        this.initMap();
        this.loadMapSources();
        this.bindEvents();
        this.initModal();
        this.logStatus('Application initialized');
    }
    
    initTheme() {
        const savedTheme = localStorage.getItem('theme') || 'light';
        this.isDarkMode = savedTheme === 'dark';
        this.applyTheme();
    }
    
    toggleTheme() {
        this.isDarkMode = !this.isDarkMode;
        this.applyTheme();
        localStorage.setItem('theme', this.isDarkMode ? 'dark' : 'light');
    }
    
    applyTheme() {
        const html = document.documentElement;
        const themeToggle = document.getElementById('themeToggle');
        const icon = themeToggle.querySelector('i');
        
        if (this.isDarkMode) {
            html.setAttribute('data-bs-theme', 'dark');
            icon.className = 'fas fa-sun';
        } else {
            html.setAttribute('data-bs-theme', 'light');
            icon.className = 'fas fa-moon';
        }
    }
    
    initMap() {
        this.map = L.map('map').setView([39.8283, -98.5795], 4);
        L.tileLayer('https://{s}.tile.openstreetmap.org/{z}/{x}/{y}.png', {
            attribution: '© OpenStreetMap contributors',
            maxZoom: 19
        }).addTo(this.map);
        
        this.initDrawControls();
        this.logStatus('Map initialized');
    }
    
    initDrawControls() {
        const drawnItems = new L.FeatureGroup();
        this.map.addLayer(drawnItems);
        
        this.drawControl = new L.Control.Draw({
            position: 'topright',
            draw: {
                polygon: false,
                polyline: false,
                circle: false,
                marker: false,
                circlemarker: false,
                rectangle: {
                    shapeOptions: {
                        color: '#3498db',
                        weight: 2,
                        fillOpacity: 0.2
                    }
                }
            },
            edit: {
                featureGroup: drawnItems,
                remove: true
            }
        });
        
        this.map.addControl(this.drawControl);
        
        this.map.on(L.Draw.Event.CREATED, (e) => {
            const layer = e.layer;
            drawnItems.addLayer(layer);
            
            if (e.layerType === 'rectangle') {
                this.selectedArea = layer.getBounds();
                this.updateSelectionInfo();
                this.enableEstimation();
            }
        });
        
        this.map.on(L.Draw.Event.DELETED, () => {
            this.selectedArea = null;
            this.updateSelectionInfo();
            this.disableEstimation();
        });
    }
    
    // followJob opens the SSE stream of the job and updates the UI from its events.
    followJob(id) {
        this.jobEvents = new EventSource('/api/jobs/' + encodeURIComponent(id) + '/events');
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
                this.resetDownloadUI();
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

    onJobState(record) {
        switch (record.state) {
            case 'running':
                this.isDownloading = true;
                this.showCancelButton(true);
                break;
            case 'complete':
                this.closeJobEvents();
                this.logStatus('Download complete. Saved to ' + record.output_file, 'success');
                this.showDownloadComplete(record);
                this.resetDownloadUI();
                break;
            case 'failed':
                this.closeJobEvents();
                // A failed job sends its error as a log event first, and a client that connects late gets only this state.
                if (record.error !== this.lastJobLog) {
                    this.logStatus('Download failed: ' + record.error, 'error');
                }
                this.hideDownloadResults();
                this.resetDownloadUI();
                break;
            case 'cancelled':
                this.closeJobEvents();
                this.logStatus('Download cancelled', 'warning');
                this.resetDownloadUI();
                break;
        }
    }

    async loadMapSources() {
        try {
            const response = await fetch('/api/sources');
            const sources = await response.json();
            
            if (response.ok) {
                this.mapSources = sources;
                this.populateSourceSelect();
                this.logStatus('Loaded ' + sources.length + ' map sources');
            } else {
                throw new Error((sources.error && sources.error.message) || 'Failed to load sources');
            }
        } catch (error) {
            this.logStatus('Error loading sources: ' + error.message, 'error');
        }
    }
    
    populateSourceSelect() {
        const select = document.getElementById('mapSourceSelect');
        select.innerHTML = '<option value="">Select a map source...</option>';
        
        this.mapSources.forEach(source => {
            const option = document.createElement('option');
            option.value = source.name;
            option.textContent = `${source.name} (${source.format}) - Zoom ${source.min_zoom}-${source.max_zoom}`;
            option.title = `${source.name} - Format: ${source.format} - Zoom range: ${source.min_zoom} to ${source.max_zoom}`;
            select.appendChild(option);
        });
    }
    
    bindEvents() {
        document.getElementById('themeToggle').addEventListener('click', () => this.toggleTheme());
        document.getElementById('drawRectangle').addEventListener('click', () => {
            new L.Draw.Rectangle(this.map).enable();
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
        document.getElementById('minZoom').addEventListener('change', () => this.validateZoomLevels());
        document.getElementById('maxZoom').addEventListener('change', () => this.validateZoomLevels());
    }
    
    clearSelection() {
        this.map.eachLayer((layer) => {
            if (layer instanceof L.Rectangle || layer instanceof L.Polygon) {
                this.map.removeLayer(layer);
            }
        });
        this.selectedArea = null;
        this.updateSelectionInfo();
        this.disableEstimation();
        this.clearPreview();
        this.logStatus('Selection cleared');
    }
    
    updateSelectionInfo() {
        const infoDiv = document.getElementById('selectionInfo');
        if (this.selectedArea) {
            const bounds = this.selectedArea;
            const area = this.calculateArea(bounds);
            infoDiv.innerHTML = `
                <strong>Selected Area:</strong><br>
                North: ${bounds.getNorth().toFixed(6)}°<br>
                South: ${bounds.getSouth().toFixed(6)}°<br>
                East: ${bounds.getEast().toFixed(6)}°<br>
                West: ${bounds.getWest().toFixed(6)}°<br>
                <small>Area: ~${area.toFixed(2)} km²</small>
            `;
        } else {
            infoDiv.innerHTML = 'Click "Draw Rectangle" to select an area on the map';
        }
    }
    
    calculateArea(bounds) {
        const lat1 = bounds.getSouth();
        const lat2 = bounds.getNorth();
        const lon1 = bounds.getWest();
        const lon2 = bounds.getEast();
        return Math.abs((lon2 - lon1) * (lat2 - lat1)) * 111.32 * 111.32;
    }
    
    enableEstimation() {
        document.getElementById('estimateBtn').disabled = false;
        document.getElementById('previewBtn').disabled = false;
    }
    
    disableEstimation() {
        document.getElementById('estimateBtn').disabled = true;
        document.getElementById('previewBtn').disabled = true;
        document.getElementById('downloadBtn').disabled = true;
        document.getElementById('estimationResult').innerHTML = '';
        this.clearPreview();
    }
    
    async estimateDownload() {
        if (!this.selectedArea || !document.getElementById('mapSourceSelect').value) {
            this.logStatus('Select area and map source first', 'error');
            return;
        }
        const minZoom = parseInt(document.getElementById('minZoom').value);
        const maxZoom = parseInt(document.getElementById('maxZoom').value);
        if (minZoom >= maxZoom) {
            this.logStatus('Invalid zoom range', 'error');
            return;
        }
        try {
            const bounds = this.selectedArea;
            const response = await fetch('/api/estimate', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({
                    bounds: [bounds.getWest(), bounds.getSouth(), bounds.getEast(), bounds.getNorth()],
                    min_zoom: minZoom,
                    max_zoom: maxZoom
                })
            });
            const result = await response.json();
            if (response.ok) {
                this.displayEstimation(result);
                document.getElementById('downloadBtn').disabled = false;
                this.logStatus('Download estimation completed');
            } else {
                throw new Error((result.error && result.error.message) || 'Estimation failed');
            }
        } catch (error) {
            this.logStatus('Estimation error: ' + error.message, 'error');
        }
    }
    
    async previewTiles() {
        if (!this.selectedArea || !document.getElementById('mapSourceSelect').value) {
            this.logStatus('Select area and map source first', 'error');
            return;
        }
        const minZoom = parseInt(document.getElementById('minZoom').value);
        const maxZoom = parseInt(document.getElementById('maxZoom').value);
        if (minZoom >= maxZoom) {
            this.logStatus('Invalid zoom range', 'error');
            return;
        }
        try {
            this.clearPreview();
            this.logStatus('Loading preview tiles...', 'info');
            const bounds = this.selectedArea;
            const response = await fetch('/api/preview', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({
                    source: document.getElementById('mapSourceSelect').value,
                    bounds: [bounds.getWest(), bounds.getSouth(), bounds.getEast(), bounds.getNorth()],
                    min_zoom: minZoom,
                    max_zoom: maxZoom
                })
            });
            const result = await response.json();
            if (response.ok) {
                this.displayPreview(result);
                this.logStatus('Preview loaded: ' + result.total_tiles + ' tiles at zoom ' + result.zoom);
            } else {
                throw new Error((result.error && result.error.message) || 'Preview failed');
            }
        } catch (error) {
            this.logStatus('Preview error: ' + error.message, 'error');
        }
    }
    
    displayPreview(previewData) {
        this.previewLayer = L.layerGroup().addTo(this.map);
        previewData.tiles.forEach(tile => {
            const bounds = L.latLngBounds(
                [tile.bounds[1], tile.bounds[0]],
                [tile.bounds[3], tile.bounds[2]]
            );
            const imageOverlay = L.imageOverlay(tile.url, bounds, { opacity: 0.8, crossOrigin: 'anonymous' });
            this.previewLayer.addLayer(imageOverlay);
        });
        const estimationResult = document.getElementById('estimationResult');
        let previewInfo = document.getElementById('previewInfo');
        if (previewInfo) previewInfo.remove();
        previewInfo = document.createElement('div');
        previewInfo.id = 'previewInfo';
        previewInfo.className = 'alert alert-info mt-2';
        previewInfo.innerHTML = `
            <strong>Preview:</strong> ${previewData.total_tiles} tiles from "${previewData.source}"<br>
            <small>Zoom Level ${previewData.zoom}</small><br>
            <button type="button" class="btn btn-sm btn-outline-secondary mt-1" onclick="app.clearPreview()">
                <i class="fas fa-times"></i> Clear Preview
            </button>
        `;
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
                this.searchMarker = L.marker([first.lat, first.lon])
                    .addTo(this.map)
                    .bindPopup('<strong>' + first.display_name + '</strong><br><small>Lat: ' + first.lat.toFixed(6) + ', Lon: ' + first.lon.toFixed(6) + '</small>')
                    .openPopup();
                this.logStatus('Found: ' + first.display_name, 'success');
            } else {
                this.logStatus('No results found for "' + query + '"', 'warning');
            }
        } catch (error) {
            this.logStatus('Search error: ' + error.message, 'error');
        }
    }
    
    displayEstimation(result) {
        const resultDiv = document.getElementById('estimationResult');
        let className = 'success';
        let warningText = '';
        if (result.total_tiles > 10000) {
            className = 'warning';
            warningText = '⚠️ Large download - may take significant time';
        }
        if (result.total_tiles > 100000) {
            className = 'error';
            warningText = '⚠️ Very large download! Consider reducing zoom range';
        }
        const estimatedSeconds = result.total_tiles / 10;
        const estimatedTime = this.formatTime(estimatedSeconds);
        const formattedSize = result.estimated_size_mb >= 1024 ? 
            (result.estimated_size_mb / 1024).toFixed(2) + ' GB' : result.estimated_size_mb.toFixed(2) + ' MB';
        resultDiv.className = className;
        resultDiv.innerHTML = `
            <strong>Download Estimation:</strong><br>
            Total Tiles: ${result.total_tiles.toLocaleString()}<br>
            Estimated Size: ${formattedSize}<br>
            Zoom Range: ${result.zoom_range[0]} - ${result.zoom_range[1]}<br>
            Est. Time: ~${estimatedTime}<br>
            ${warningText ? '<small class="text-warning">' + warningText + '</small>' : ''}
        `;
    }
    
    formatTime(seconds) {
        if (seconds < 60) return Math.round(seconds) + 's';
        if (seconds < 3600) return Math.round(seconds/60) + 'm';
        if (seconds < 86400) return Math.round(seconds/3600) + 'h';
        return Math.round(seconds/86400) + 'd';
    }
    
    async startDownload() {
        if (!this.selectedArea || !document.getElementById('mapSourceSelect').value) {
            this.logStatus('Select area and map source first', 'error');
            return;
        }
        const bounds = this.selectedArea;
        const concurrency = parseInt(document.getElementById('maxConcurrent').value) || 5;

        this.hideDownloadResults();
        document.getElementById('downloadBtn').disabled = true;
        document.getElementById('downloadBtn').innerHTML = '<i class="fas fa-spinner fa-spin"></i> Downloading...';
        try {
            const response = await fetch('/api/jobs', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({
                    source: document.getElementById('mapSourceSelect').value,
                    bounds: [bounds.getWest(), bounds.getSouth(), bounds.getEast(), bounds.getNorth()],
                    min_zoom: parseInt(document.getElementById('minZoom').value),
                    max_zoom: parseInt(document.getElementById('maxZoom').value),
                    name: document.getElementById('customName').value.trim(),
                    concurrency: concurrency,
                    failure_tolerance: parseFloat(document.getElementById('failureTolerance').value) || 0.3,
                    ignore_errors: document.getElementById('ignoreErrors').checked,
                    verify_tls: document.getElementById('verifyTLS').checked
                })
            });
            const result = await response.json();
            if (!response.ok) {
                throw new Error((result.error && result.error.message) || 'The server did not start the job');
            }
            this.jobId = result.id;
            this.lastJobLog = null;
            this.logStatus('Download started with ' + concurrency + ' concurrent connections', 'info');
            this.logStatus('Output file: ' + result.output_file, 'info');
            this.followJob(result.id);
        } catch (error) {
            this.logStatus('Download error: ' + error.message, 'error');
            this.resetDownloadUI();
        }
    }

    updateDownloadProgress(data) {
        const progressDiv = document.getElementById('downloadProgress');
        const progress = data.total > 0 ? Math.min(100, data.downloaded / data.total * 100) : 0;
        let etaText = data.eta_seconds > 0 ? ' - ETA: ' + this.formatTime(data.eta_seconds) : '';
        let rateText = data.rate && data.rate > 0 ? ' (' + data.rate.toFixed(1) + ' tiles/s)' : '';
        let failedText = '';
        if (data.failed > 0) {
            const failureRate = ((data.failed / (data.downloaded + data.failed)) * 100).toFixed(1);
            failedText = '<br><small class="text-warning">⚠️ Failed tiles: ' + data.failed + ' (' + failureRate + '% failure rate)</small>';
        }
        let statusText = '<small>Downloaded ' + data.downloaded + '/' + data.total + ' tiles (' + progress.toFixed(1) + '%)' + rateText + etaText + '</small>';
        
        progressDiv.innerHTML = `
            <div class="progress mb-2">
                <div class="progress-bar ${progress >= 100 ? 'bg-success' : ''}" role="progressbar" style="width: ${progress}%">
                    ${progress.toFixed(1)}%
                </div>
            </div>
            ${statusText}
            ${failedText}
        `;
    }
    
    resetDownloadUI() {
        document.getElementById('downloadBtn').disabled = false;
        document.getElementById('downloadBtn').innerHTML = '<i class="fas fa-download"></i> Start Download';
        document.getElementById('downloadProgress').innerHTML = '';
        this.showCancelButton(false);
        this.isDownloading = false;
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
        if (minZoom >= maxZoom) {
            document.getElementById('maxZoom').value = minZoom + 1;
        }
    }
    
    logStatus(message, type) {
        const statusLog = document.getElementById('statusLog');
        const timestamp = new Date().toLocaleTimeString();
        const className = type === 'error' ? 'text-danger' : type === 'success' ? 'text-success' : type === 'warning' ? 'text-warning' : '';
        const logEntry = document.createElement('div');
        logEntry.className = className;
        logEntry.textContent = '[' + timestamp + '] ' + message;
        statusLog.appendChild(logEntry);
        statusLog.scrollTop = statusLog.scrollHeight;
        while (statusLog.children.length > 50) statusLog.removeChild(statusLog.firstChild);
    }
    
    showDownloadComplete(record) {
        document.getElementById('downloadResultMessage').textContent = 'Downloaded ' + record.downloaded + ' of ' + record.total + ' tiles.';
        document.getElementById('downloadResultFile').textContent = record.output_file;
        document.getElementById('downloadResults').style.display = 'block';
        document.getElementById('downloadResults').scrollIntoView({ behavior: 'smooth' });
    }
    
    hideDownloadResults() {
        document.getElementById('downloadResults').style.display = 'none';
    }
    
    initModal() {
        const modalElement = document.getElementById('loadingModal');
        if (modalElement) {
            this.loadingModal = new bootstrap.Modal(modalElement, { backdrop: 'static', keyboard: false });
        }
    }
    
    // cancelDownload asks the server to cancel the job. The final state event resets the UI.
    async cancelDownload() {
        if (!this.isDownloading || !this.jobId) {
            return;
        }
        this.logStatus('Cancelling download...', 'warning');
        document.getElementById('cancelBtn').disabled = true;
        document.getElementById('cancelBtn').innerHTML = '<i class="fas fa-spinner fa-spin"></i> Cancelling...';
        try {
            const response = await fetch('/api/jobs/' + encodeURIComponent(this.jobId), { method: 'DELETE' });
            if (!response.ok) {
                const result = await response.json();
                throw new Error((result.error && result.error.message) || 'The server did not cancel the job');
            }
        } catch (error) {
            this.logStatus('Cancel error: ' + error.message, 'error');
            this.showCancelButton(true);
        }
    }
    
    showCancelButton(show) {
        const cancelBtn = document.getElementById('cancelBtn');
        cancelBtn.style.display = show ? 'block' : 'none';
        cancelBtn.disabled = false;
        cancelBtn.innerHTML = '<i class="fas fa-times"></i> Cancel';
    }
}

document.addEventListener('DOMContentLoaded', () => {
    window.app = new ATAKDownloader();
});
