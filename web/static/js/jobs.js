// jobs.js runs the Jobs page: the queue, the history, the job detail drawer and the server footer (FR-team-server-25 to 28).
// It inserts every server string with textContent, so a source name or a label never becomes markup.
'use strict';

// ACTIVE_STATES are the job states of the queue table. Every other state is in the history table.
const ACTIVE_STATES = ['queued', 'running'];
// REFRESH_MS is the reload interval of the job list while a job is queued or running (PM ruling on #59).
const REFRESH_MS = 5000;
// SHORT_ID is the length of the short job id in a row (FR-team-server-26).
const SHORT_ID = 6;

// jobName returns the area name or label of a row: the label, else the name of the first area, else a dash (PM ruling on #59).
function jobName(record) {
    return record.label || record.area_name || '—';
}

// jobProgress returns the percent of the tiles that the output file holds.
function jobProgress(record) {
    return record.total > 0 ? Math.min(100, Math.round((record.downloaded + record.skipped) / record.total * 100)) : 0;
}

// jobActions returns the actions that the state of record allows (FR-team-server-27).
// files holds the names of the output files on the server, because a failed or cancelled job can have no output file.
function jobActions(record, files) {
    if (ACTIVE_STATES.includes(record.state)) return ['cancel'];
    if (record.state === 'complete') return ['download'];
    return files.has(record.output_file) ? ['resume', 'download'] : ['resume'];
}

// formatTime returns a time of a job record for a person, or a dash for a time that is not set.
function formatTime(value) {
    return value ? new Date(value).toLocaleString() : '—';
}

class JobsPage {
    constructor() {
        this.page = 1;
        this.pages = 1;
        this.loaded = false;
        this.files = new Set();
        this.timer = null;
        this.events = null;
        this.drawerRecord = null;
        this.retryRequest = null;
    }

    // start binds the controls and loads the job list and the server footer.
    start() {
        document.getElementById('signOut').addEventListener('click', () => this.signOut());
        document.getElementById('errorRetry').addEventListener('click', () => this.retry());
        document.getElementById('drawerClose').addEventListener('click', () => this.closeDrawer());
        document.getElementById('pageNewer').addEventListener('click', () => this.goToPage(this.page - 1));
        document.getElementById('pageOlder').addEventListener('click', () => this.goToPage(this.page + 1));
        return Promise.all([this.load(), this.loadConfig()]);
    }

    // request sends a request and returns the JSON body of a 2xx response, or null for a response with no body.
    // A 401 means that the token cookie is gone, so the page goes to the sign-in page (FR-team-server-23).
    // For another error status, it throws an Error with the error.message of the server.
    async request(path, options) {
        let response;
        try {
            response = await fetch(path, options);
        } catch {
            throw new Error('Server unreachable');
        }
        if (response.status === 401) {
            window.location.assign('/signin');
            const error = new Error('Sign in again');
            error.signedOut = true;
            throw error;
        }
        // A proxy can send an error page that is not JSON, so a parse failure keeps the status as the message.
        const body = await response.json().catch(() => null);
        if (!response.ok) {
            throw new Error((body && body.error && body.error.message) || 'The server returned status ' + response.status);
        }
        return body;
    }

    // load reads one page of the job list and the output files, and shows them.
    async load() {
        clearTimeout(this.timer);
        this.timer = null;
        let list;
        try {
            list = await this.request('/api/jobs?page=' + this.page);
            // The output file list tells whether a failed or cancelled job has a file to download.
            const files = await this.request('/api/files');
            this.files = new Set(files.map(f => f.name));
        } catch (error) {
            this.fail('The job list did not load: ' + error.message, error, () => this.load());
            return;
        }
        this.hideError();
        this.loaded = true;
        this.page = list.page;
        this.pages = list.pages;
        this.render(list.jobs);
        // The list holds no event stream, so the page reloads it while a job can change state (PM ruling on #59).
        if (list.jobs.some(j => ACTIVE_STATES.includes(j.state))) {
            // Two loads can overlap, for example a timer and an action, so the second clears the timer of the first.
            clearTimeout(this.timer);
            this.timer = setTimeout(() => this.load(), REFRESH_MS);
        }
    }

    goToPage(page) {
        this.page = Math.max(1, Math.min(page, this.pages));
        this.load();
    }

    // render shows the jobs in the queue table and the history table, or the empty state.
    render(jobs) {
        document.getElementById('jobsLoading').hidden = true;
        const queue = jobs.filter(j => ACTIVE_STATES.includes(j.state));
        const history = jobs.filter(j => !ACTIVE_STATES.includes(j.state));
        // A page after the last page has no jobs, and the pager must stay so that the user can go back.
        const empty = jobs.length === 0 && this.pages <= 1;
        document.getElementById('jobsEmpty').hidden = !empty;
        document.getElementById('jobsTables').hidden = empty;
        document.getElementById('queueRows').replaceChildren(...queue.map(j => this.row(j)));
        document.getElementById('historyRows').replaceChildren(...history.map(j => this.row(j)));
        document.getElementById('queueEmpty').hidden = queue.length > 0;
        document.getElementById('historyEmpty').hidden = history.length > 0;
        document.getElementById('jobsStatus').textContent = empty ? '' :
            queue.length + ' queued or running, ' + history.length + ' finished';
        document.getElementById('jobsPager').hidden = this.pages <= 1;
        document.getElementById('pageInfo').textContent = 'Page ' + this.page + ' of ' + this.pages;
        document.getElementById('pageNewer').disabled = this.page <= 1;
        document.getElementById('pageOlder').disabled = this.page >= this.pages;
    }

    // row returns the table row of a job with the fields of FR-team-server-26 and the actions of FR-team-server-27.
    row(record) {
        const shortID = record.id.slice(0, SHORT_ID);
        const tr = document.createElement('tr');
        tr.dataset.id = record.id;
        const cell = (...nodes) => {
            const td = document.createElement('td');
            td.append(...nodes);
            tr.append(td);
            return td;
        };

        const open = document.createElement('button');
        open.type = 'button';
        open.className = 'link-button job-id';
        open.textContent = shortID;
        open.setAttribute('aria-label', 'Show the details of job ' + shortID);
        open.addEventListener('click', () => this.openDrawer(record));
        cell(open);
        cell(record.source);
        cell(jobName(record));
        cell(record.min_zoom + '–' + record.max_zoom);
        cell(this.stateChip(record.state));
        cell(...this.progress(record, shortID));
        cell(formatTime(record.created_at));
        cell(...jobActions(record, this.files).map(a => this.action(a, record)));
        return tr;
    }

    stateChip(state) {
        const chip = document.createElement('span');
        chip.className = 'chip state-' + state;
        chip.textContent = state;
        return chip;
    }

    // progress returns the progress bar and the percent of a job, or the queue position of a queued job.
    progress(record, shortID) {
        if (record.state === 'queued') {
            return [record.position ? 'Position ' + record.position : '—'];
        }
        const percent = jobProgress(record);
        const bar = document.createElement('span');
        bar.className = 'mini-bar';
        bar.setAttribute('role', 'progressbar');
        bar.setAttribute('aria-label', 'Progress of job ' + shortID);
        bar.setAttribute('aria-valuemin', '0');
        bar.setAttribute('aria-valuemax', '100');
        bar.setAttribute('aria-valuenow', String(percent));
        const fill = document.createElement('span');
        fill.className = 'mini-bar-fill';
        fill.style.width = percent + '%';
        bar.append(fill);
        return [bar, percent + '%'];
    }

    // action returns the control of one row action.
    action(name, record) {
        if (name === 'download') {
            const link = document.createElement('a');
            link.className = 'btn btn-sm';
            link.href = '/api/files/' + encodeURIComponent(record.output_file);
            link.textContent = 'Download';
            return link;
        }
        const button = document.createElement('button');
        button.type = 'button';
        button.className = name === 'cancel' ? 'btn btn-sm btn-danger' : 'btn btn-sm';
        button.textContent = name === 'cancel' ? 'Cancel' : 'Resume';
        button.addEventListener('click', () => (name === 'cancel' ? this.cancel(record.id) : this.resume(record.id)));
        return button;
    }

    // cancel sends DELETE /api/jobs/{id}. A queued job leaves the queue, and a running job stops (FR-team-server-4).
    async cancel(id) {
        await this.send('DELETE', '/api/jobs/' + encodeURIComponent(id), 'The job did not cancel: ', () => this.cancel(id));
    }

    // resume sends POST /api/jobs/{id}/resume, which queues the job again for its missing tiles.
    async resume(id) {
        await this.send('POST', '/api/jobs/' + encodeURIComponent(id) + '/resume', 'The job did not resume: ', () => this.resume(id));
    }

    // send sends one job action and loads the list again, because the action changes the state of the job.
    async send(method, path, prefix, retry) {
        try {
            await this.request(path, { method });
        } catch (error) {
            this.fail(prefix + error.message, error, retry);
            return;
        }
        this.hideError();
        await this.load();
    }

    // signOut deletes the token cookie and goes to the sign-in page (PM ruling on #59).
    async signOut() {
        try {
            await this.request('/api/session', { method: 'DELETE' });
        } catch (error) {
            this.fail('Sign out failed: ' + error.message, error, () => this.signOut());
            return;
        }
        window.location.assign('/signin');
    }

    // openDrawer shows the record of a job and follows its event stream (FR-team-server-28).
    openDrawer(record) {
        this.closeDrawer();
        this.drawerRecord = record;
        document.getElementById('drawerTitle').textContent = 'Job ' + record.id.slice(0, SHORT_ID);
        document.getElementById('drawerLog').replaceChildren();
        this.renderFields();
        document.getElementById('jobDrawer').hidden = false;

        const events = new EventSource('/api/jobs/' + encodeURIComponent(record.id) + '/events');
        this.events = events;
        // The first event is a state event with the current record, so the drawer shows fresh counts at once.
        events.addEventListener('state', (e) => {
            this.drawerRecord = JSON.parse(e.data);
            this.renderFields();
            if (!ACTIVE_STATES.includes(this.drawerRecord.state)) {
                // EventSource reconnects to an ended stream forever, so the page closes it at the final state.
                this.closeEvents();
                this.load();
            }
        });
        events.addEventListener('progress', (e) => {
            Object.assign(this.drawerRecord, JSON.parse(e.data));
            this.renderFields();
        });
        events.addEventListener('log', (e) => this.appendLog(JSON.parse(e.data)));
        events.onerror = () => {
            // CLOSED means that the server refused the stream. Else EventSource reconnects by itself.
            if (this.events === events && events.readyState === EventSource.CLOSED) {
                this.appendLog({ level: 'error', message: 'The event stream closed' });
                this.closeEvents();
            }
        };
    }

    closeDrawer() {
        this.closeEvents();
        this.drawerRecord = null;
        document.getElementById('jobDrawer').hidden = true;
    }

    closeEvents() {
        if (this.events) {
            this.events.close();
            this.events = null;
        }
    }

    // renderFields shows the fields of the job record in the drawer.
    renderFields() {
        const r = this.drawerRecord;
        const fields = [
            ['Job id', r.id],
            ['State', r.state],
            ['Map source', r.source],
            ['Area or label', jobName(r)],
            ['Zoom range', r.min_zoom + '–' + r.max_zoom],
            ['Output file', r.output_file],
            ['Tiles', (r.downloaded + r.skipped) + ' / ' + r.total + ' · ' + r.downloaded + ' downloaded · ' + r.skipped + ' skipped · ' + r.failed + ' failed'],
            ['Created', formatTime(r.created_at)],
            ['Started', formatTime(r.started_at)],
            ['Finished', formatTime(r.finished_at)]
        ];
        if (r.state === 'queued' && r.position) fields.splice(2, 0, ['Queue position', String(r.position)]);
        if (r.error) fields.push(['Error', r.error]);
        if (r.reason) fields.push(['Reason', r.reason]);
        const nodes = [];
        for (const [name, value] of fields) {
            const dt = document.createElement('dt');
            dt.textContent = name;
            const dd = document.createElement('dd');
            dd.textContent = value;
            nodes.push(dt, dd);
        }
        document.getElementById('drawerFields').replaceChildren(...nodes);
    }

    // appendLog adds one log event to the drawer log.
    appendLog(entry) {
        const line = document.createElement('div');
        line.className = 'job-log-line ' + entry.level;
        line.textContent = new Date().toLocaleTimeString() + ' ' + entry.level + ': ' + entry.message;
        const log = document.getElementById('drawerLog');
        log.append(line);
        log.scrollTop = log.scrollHeight;
    }

    // loadConfig shows the server footer: the version, the bind address, the data directory and the worker count.
    async loadConfig() {
        let config;
        try {
            config = await this.request('/api/config');
        } catch {
            // The job list shows the error of the server, so the footer stays empty.
            return;
        }
        const parts = [];
        if (config.version) parts.push('atak-dl ' + config.version);
        parts.push('Bind ' + config.addr, 'Data directory ' + config.data_dir, config.workers + (config.workers === 1 ? ' worker' : ' workers'));
        document.getElementById('serverFooter').replaceChildren(...parts.map(text => {
            const span = document.createElement('span');
            span.textContent = text;
            return span;
        }));
    }

    // fail shows message in the error banner, and Retry calls retry. A 401 already left the page, so it shows nothing.
    fail(message, error, retry) {
        if (error.signedOut) return;
        if (!this.loaded) {
            document.getElementById('jobsLoading').hidden = true;
            document.getElementById('jobsStatus').textContent = '';
        }
        this.retryRequest = retry;
        document.getElementById('errorMessage').textContent = message;
        document.getElementById('errorBanner').hidden = false;
    }

    hideError() {
        this.retryRequest = null;
        document.getElementById('errorBanner').hidden = true;
    }

    retry() {
        const retry = this.retryRequest;
        this.hideError();
        if (retry) retry();
    }
}

document.addEventListener('DOMContentLoaded', () => {
    new JobsPage().start();
});
