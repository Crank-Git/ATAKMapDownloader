// signin.js sends the token of the sign-in page to POST /api/session (FR-team-server-20).
'use strict';

// SIGNIN_MESSAGES maps the refusals of POST /api/session to the text that the page shows (feature 12, Screens & states).
const SIGNIN_MESSAGES = {
    401: 'Wrong token',
    429: 'Too many attempts. Wait one minute, then try again.'
};

class SigninPage {
    start() {
        document.getElementById('signinForm').addEventListener('submit', (e) => {
            e.preventDefault();
            this.signIn();
        });
    }

    // signIn sends the token. On 204 the server set the token cookie, so the page goes to the Jobs page.
    async signIn() {
        // A pasted token often carries a space at an end, and a generated token holds none.
        const token = document.getElementById('token').value.trim();
        if (!token) {
            this.showError('Enter the token');
            return;
        }
        const button = document.getElementById('signinButton');
        button.disabled = true;
        this.showError('');
        let response;
        try {
            response = await fetch('/api/session', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ token })
            });
        } catch {
            this.showError('Server unreachable');
            button.disabled = false;
            return;
        }
        button.disabled = false;
        if (response.ok) {
            window.location.assign('/jobs');
            return;
        }
        this.showError(SIGNIN_MESSAGES[response.status] || 'Sign in failed: the server returned status ' + response.status);
    }

    // showError shows message under the token field. An empty message hides the field error.
    showError(message) {
        const error = document.getElementById('signinError');
        error.textContent = message;
        error.hidden = !message;
    }
}

document.addEventListener('DOMContentLoaded', () => {
    new SigninPage().start();
});
