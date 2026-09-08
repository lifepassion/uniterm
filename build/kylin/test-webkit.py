"""Exercise the real WebKit 4.0 custom-scheme transport under Xvfb."""
import base64
import pathlib
import sys
import gi
gi.require_version('Gtk', '3.0')
gi.require_version('WebKit2', '4.0')
from gi.repository import Gtk, WebKit2, Gio, GLib

result = 1
context = WebKit2.WebContext.new_ephemeral()
security = context.get_security_manager()
security.register_uri_scheme_as_secure('wails')
security.register_uri_scheme_as_cors_enabled('wails')
shim = (pathlib.Path(__file__).parent / 'legacy-fetch.js').read_text()
html = '''<!doctype html><html><body><script>
''' + shim + '''
(async () => {
  for (const n of [1, 1024, 524288]) {
    const data = new Uint8Array(n);
    for (let i = 0; i < n; i++) data[i] = i % 256;
    const response = await fetch('/echo', {method:'POST', body:data});
    const got = new Uint8Array(await response.arrayBuffer());
    if (got.length !== data.length || got.some((v, i) => v !== data[i])) throw Error('binary mismatch: ' + n);
  }
  const text = '中文终端🙂';
  const response = await fetch('/echo', {method:'POST', body: text});
  if (await response.text() !== text) throw Error('Unicode mismatch');
  window.webkit.messageHandlers.test.postMessage('PASS');
})().catch(e => window.webkit.messageHandlers.test.postMessage('FAIL: ' + e));
</script></body></html>'''

def request(req):
    try:
        if req.get_path() == '/echo':
            encoded = req.get_http_headers().get_one('X-Uniterm-Legacy-Body')
            data = base64.b64decode(encoded or '', validate=True)
            content_type = 'application/octet-stream'
        else:
            data = html.encode()
            content_type = 'text/html; charset=utf-8'
        stream = Gio.MemoryInputStream.new_from_bytes(GLib.Bytes.new(data))
        req.finish(stream, len(data), content_type)
    except Exception as exc:
        print('FAIL:', exc, flush=True)
        Gtk.main_quit()

def message(manager, value):
    global result
    text = value.get_js_value().to_string()
    print(text, flush=True)
    result = 0 if text == 'PASS' else 1
    Gtk.main_quit()

context.register_uri_scheme('wails', request)
view = WebKit2.WebView.new_with_context(context)
manager = view.get_user_content_manager()
manager.register_script_message_handler('test')
manager.connect('script-message-received::test', message)
window = Gtk.Window()
window.add(view)
window.show_all()
view.load_uri('wails://wails.localhost/index.html')
def timeout():
    print('FAIL: WebKit test timed out', flush=True)
    Gtk.main_quit()
    return False
GLib.timeout_add_seconds(40, timeout)
Gtk.main()
window.destroy()
sys.exit(result)
