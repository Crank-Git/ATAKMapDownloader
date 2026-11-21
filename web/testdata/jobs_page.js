// jobs_page.js runs web/static/js/jobs.js in Node with a fake DOM from jobs.html, applies one scenario and prints the page as JSON.
// Usage: node jobs_page.js <jobs.html> <jobs.js> <scenario JSON>
// The scenario holds the server responses by "METHOD path" and the steps of the user, in order.
'use strict';

const fs = require('fs');
const vm = require('vm');
const { fakeDocument, fakeFetch, flush } = require('./fake_dom.js');

const [htmlPath, scriptPath, scenarioJSON] = process.argv.slice(2);
const scenario = JSON.parse(scenarioJSON);

const document = fakeDocument(htmlPath);
const requests = [];
const location = { assigned: '', assign(url) { this.assigned = url; } };

// The timers do not run by themselves, so a step "tick" runs the timers that are due.
const timers = [];
function setTimeout(fn, ms) { timers.push({ fn, ms, done: false }); return timers.length; }
function clearTimeout(id) { if (timers[id - 1]) timers[id - 1].done = true; }

// FakeEventSource records its URL and its listeners, so a step "event" can send an event to it.
const streams = [];
class FakeEventSource {
    constructor(url) {
        this.url = url;
        this.closed = false;
        this.listeners = {};
        this.readyState = FakeEventSource.OPEN;
        streams.push(this);
    }
    addEventListener(type, fn) { (this.listeners[type] = this.listeners[type] || []).push(fn); }
    close() { this.closed = true; this.readyState = FakeEventSource.CLOSED; }
}
FakeEventSource.OPEN = 1;
FakeEventSource.CLOSED = 2;

const context = vm.createContext({
    document,
    window: { location },
    fetch: fakeFetch(scenario.responses || {}, requests),
    EventSource: FakeEventSource,
    setTimeout,
    clearTimeout,
    console
});
vm.runInContext(fs.readFileSync(scriptPath, 'utf8'), context, { filename: scriptPath });
vm.runInContext('globalThis.JobsPage = JobsPage;', context);

const el = id => document.getElementById(id);

// rowByID returns the table row of the job id from the queue or the history.
function rowByID(id) {
    const row = [...el('queueRows').children, ...el('historyRows').children].find(r => r.dataset.id === id);
    if (!row) throw new Error('no row for the job ' + id);
    return row;
}

// buttonIn returns the first element under node with the text, or null.
function buttonIn(node, text) {
    for (const c of node.children || []) {
        if (typeof c === 'string') continue;
        if ((c.tagName === 'BUTTON' || c.tagName === 'A') && c.textContent === text) return c;
        const found = buttonIn(c, text);
        if (found) return found;
    }
    return null;
}

// rowOut returns the cell texts and the actions of a table row.
function rowOut(row) {
    const cells = row.children;
    const actions = cells[cells.length - 1].children.map(a => ({ text: a.textContent, href: a.href || '' }));
    return { id: row.dataset.id, cells: cells.map(c => c.textContent), actions };
}

(async () => {
    const page = new context.JobsPage();
    page.start();
    await flush();
    for (const step of scenario.steps || []) {
        switch (step.do) {
            case 'respond':
                scenario.responses[step.key] = step.response;
                break;
            case 'click': {
                const target = step.row ? buttonIn(rowByID(step.row), step.text) : el(step.element);
                if (!target) throw new Error('no button ' + step.text + ' in the row ' + step.row);
                target.dispatch('click');
                break;
            }
            case 'open':
                rowByID(step.row).children[0].children[0].dispatch('click');
                break;
            case 'event': {
                const stream = streams[streams.length - 1];
                (stream.listeners[step.name] || []).forEach(fn => fn({ data: JSON.stringify(step.data) }));
                break;
            }
            case 'tick':
                timers.filter(t => !t.done).forEach(t => { t.done = true; t.fn(); });
                break;
            default:
                throw new Error('unknown step ' + step.do);
        }
        await flush();
    }
    const visible = id => !el(id).hidden;
    const out = {
        queue: el('queueRows').children.map(rowOut),
        history: el('historyRows').children.map(rowOut),
        loading: visible('jobsLoading'),
        empty: visible('jobsEmpty'),
        tables: visible('jobsTables'),
        error: visible('errorBanner') ? el('errorMessage').textContent : '',
        requests: requests.map(r => r.key),
        location: location.assigned,
        timers: timers.filter(t => !t.done).map(t => t.ms),
        streams: streams.map(s => ({ url: s.url, closed: s.closed })),
        drawer: {
            open: visible('jobDrawer'),
            title: el('drawerTitle').textContent,
            fields: el('drawerFields').children.map(c => c.textContent),
            log: el('drawerLog').children.map(c => c.textContent)
        },
        footer: el('serverFooter').textContent,
        status: el('jobsStatus').textContent
    };
    process.stdout.write(JSON.stringify(out));
})().catch(err => {
    process.stderr.write(String(err && err.stack || err));
    process.exit(1);
});
