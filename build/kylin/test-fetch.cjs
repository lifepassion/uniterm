const fs = require('node:fs');
const vm = require('node:vm');
const assert = require('node:assert/strict');
global.location = {href: 'wails://wails.localhost/'};
let captured;
global.fetch = async (...args) => { captured = args; return new Response('ok'); };
vm.runInThisContext(fs.readFileSync(__dirname + '/legacy-fetch.js', 'utf8'));
(async () => {
    for (const body of ['中文终端🙂', new Uint8Array([0, 128, 255]), new Uint8Array(512 * 1024).fill(255), new Blob(['blob中文'])]) {
        const expected = new Uint8Array(await new Response(body).arrayBuffer());
        await fetch('/wails/runtime', {method: 'POST', body});
        const req = new Request(...captured);
        assert.equal(req.body, null);
        assert.equal(req.method, 'POST');
        assert.deepEqual(new Uint8Array(Buffer.from(req.headers.get('X-Uniterm-Legacy-Body'), 'base64')), expected);
    }
    await fetch(new Request('wails://wails.localhost/wails/runtime', {method:'POST', body:'request'}));
    assert.equal(Buffer.from(new Request(...captured).headers.get('X-Uniterm-Legacy-Body'), 'base64').toString(), 'request');
    const options = {method:'POST', body:'external'};
    await fetch('https://example.org', options);
    assert.equal(captured[1], options);
    await fetch('/index.html');
    assert.equal(new Request(...captured).headers.has('X-Uniterm-Legacy-Body'), false);
    console.log('Legacy transport: Unicode, binary, 512 KiB chunks, Blob, Request, GET and external fetch passed');
})().catch(err => { console.error(err); process.exitCode = 1; });
