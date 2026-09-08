(function () {
    'use strict';
    if (globalThis.__unitermLegacyFetch) return;
    globalThis.__unitermLegacyFetch = true;
    const original = globalThis.fetch;
    // WebKit 2.38 exposes custom-scheme headers, but not the request body.
    // Carry bytes losslessly in a private header. This never goes to HTTP hosts.
    globalThis.fetch = async function (input, init) {
        const url = new URL(input instanceof Request ? input.url : String(input), location.href);
        if (url.protocol !== 'wails:' || url.host !== new URL(location.href).host) {
            return original.apply(this, arguments);
        }
        const request = new Request(input instanceof Request ? input : url.href, init);
        if (request.method === 'GET' || request.method === 'HEAD' || !request.body) {
            return original.call(this, request);
        }
        const bytes = new Uint8Array(await request.arrayBuffer());
        let binary = '';
        for (let i = 0; i < bytes.length; i += 8192) {
            binary += String.fromCharCode.apply(null, bytes.subarray(i, i + 8192));
        }
        const headers = new Headers(request.headers);
        headers.set('X-Uniterm-Legacy-Body', btoa(binary));
        return original.call(this, request.url, {
            method: request.method, headers, signal: request.signal,
            credentials: request.credentials, mode: request.mode, cache: request.cache,
            redirect: request.redirect, referrer: request.referrer,
            referrerPolicy: request.referrerPolicy, integrity: request.integrity,
            keepalive: request.keepalive
        });
    };
})();
