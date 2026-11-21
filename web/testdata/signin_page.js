// signin_page.js runs web/static/js/signin.js in Node with a fake DOM from signin.html, submits one token and prints the page as JSON.
// Usage: node signin_page.js <signin.html> <signin.js> <scenario JSON>
// The scenario holds the token that the user types and the response of POST /api/session.
'use strict';

const fs = require('fs');
const vm = require('vm');
const { fakeDocument, fakeFetch, flush } = require('./fake_dom.js');

const [htmlPath, scriptPath, scenarioJSON] = process.argv.slice(2);
const scenario = JSON.parse(scenarioJSON);

const document = fakeDocument(htmlPath);
const requests = [];
const location = { assigned: '', assign(url) { this.assigned = url; } };
const responses = scenario.response ? { 'POST /api/session': scenario.response } : {};

const context = vm.createContext({ document, window: { location }, fetch: fakeFetch(responses, requests), console });
vm.runInContext(fs.readFileSync(scriptPath, 'utf8'), context, { filename: scriptPath });
vm.runInContext('globalThis.SigninPage = SigninPage;', context);

(async () => {
    new context.SigninPage().start();
    const el = id => document.getElementById(id);
    el('token').value = scenario.token;
    el('signinForm').dispatch('submit');
    await flush();
    process.stdout.write(JSON.stringify({
        error: el('signinError').hidden ? '' : el('signinError').textContent,
        location: location.assigned,
        requests: requests,
        buttonDisabled: el('signinButton').disabled
    }));
})().catch(err => {
    process.stderr.write(String(err && err.stack || err));
    process.exit(1);
});
