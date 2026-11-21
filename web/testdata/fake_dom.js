// fake_dom.js is the small fake DOM of the page harnesses jobs_page.js and signin_page.js.
// It takes every element with an id from the page HTML, so a script that reads a missing id fails the test.
'use strict';

const fs = require('fs');

// FakeElement holds the parts of a DOM element that jobs.js and signin.js use.
class FakeElement {
    constructor(tag, attrs) {
        this.tagName = tag.toUpperCase();
        this.attrs = attrs || {};
        this.id = this.attrs.id || '';
        this.type = this.attrs.type || '';
        this.value = this.attrs.value || '';
        this.href = this.attrs.href || '';
        this.disabled = 'disabled' in this.attrs;
        this.hidden = 'hidden' in this.attrs;
        this.className = this.attrs.class || '';
        this.children = [];
        this.listeners = {};
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
    set textContent(v) { this.text = String(v); this.children = []; }
    append(...nodes) { this.children.push(...nodes); }
    appendChild(node) { this.children.push(node); return node; }
    replaceChildren(...nodes) { this.text = ''; this.children = nodes; }
    setAttribute(name, value) { this.attrs[name] = String(value); }
    getAttribute(name) { return name in this.attrs ? this.attrs[name] : null; }
    removeAttribute(name) { delete this.attrs[name]; }
    addEventListener(type, fn) { (this.listeners[type] = this.listeners[type] || []).push(fn); }
    dispatch(type, event) { (this.listeners[type] || []).forEach(fn => fn(Object.assign({ target: this, preventDefault() {} }, event))); }
    focus() {}
    scrollTo() {}
}

// attributes returns the attributes of a tag, with a boolean attribute as an empty string.
function attributes(tag) {
    const attrs = {};
    for (const m of tag.matchAll(/([a-z-]+)(?:="([^"]*)")?/g)) attrs[m[1]] = m[2] === undefined ? '' : m[2];
    return attrs;
}

// fakeDocument returns a document with the elements of the page at htmlPath.
function fakeDocument(htmlPath) {
    const elements = {};
    const html = fs.readFileSync(htmlPath, 'utf8');
    for (const m of html.matchAll(/<([a-z0-9]+)\b([^>]*\bid="[^"]+"[^>]*)>/g)) {
        const el = new FakeElement(m[1], attributes(m[2]));
        elements[el.id] = el;
    }
    const document = {
        elements,
        getElementById(id) {
            if (!elements[id]) throw new Error(htmlPath + ' has no element with the id ' + id);
            return elements[id];
        },
        createElement: tag => new FakeElement(tag),
        createTextNode: text => text,
        addEventListener() {},
        documentElement: new FakeElement('html')
    };
    return document;
}

// flush lets every resolved promise of the page run, because the page awaits fetch and JSON parse in turn.
async function flush() {
    for (let i = 0; i < 20; i++) await new Promise(resolve => setImmediate(resolve));
}

// fakeFetch returns a fetch that answers from responses, keyed by "METHOD path", and records each request.
// A response with reject: true rejects, the same as a request that does not reach the server.
// A response with pending: true never settles, so the page stays in its loading state.
function fakeFetch(responses, requests) {
    return (path, options) => {
        const method = (options && options.method) || 'GET';
        const key = method + ' ' + path;
        requests.push({ key, body: options && options.body ? JSON.parse(options.body) : null });
        const r = responses[key] || { status: 404, body: { error: { code: 'not_found', message: 'Route not found' } } };
        if (r.reject) return Promise.reject(new TypeError('Failed to fetch'));
        if (r.pending) return new Promise(() => {});
        return Promise.resolve({
            status: r.status,
            ok: r.status >= 200 && r.status < 300,
            json: () => (r.body === undefined ? Promise.reject(new SyntaxError('no body')) : Promise.resolve(r.body))
        });
    };
}

module.exports = { FakeElement, fakeDocument, fakeFetch, flush };
