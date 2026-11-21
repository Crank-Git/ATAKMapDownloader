// theme.js applies the theme of the Jobs page and the sign-in page.
// The workspace stores the theme that the user selects, so these pages read the same value, and else follow the system.
'use strict';

(() => {
    let saved = null;
    try {
        saved = localStorage.getItem('theme');
    } catch {
        // The browser blocks storage, so the theme follows the system.
    }
    const dark = saved === 'dark' || saved === 'light' ? saved === 'dark' : window.matchMedia('(prefers-color-scheme: dark)').matches;
    document.documentElement.setAttribute('data-theme', dark ? 'dark' : 'light');
})();
