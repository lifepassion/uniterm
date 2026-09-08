"""Patch a private copy of Wails beta.16; never modify the Go module cache."""
import pathlib
import re
import sys

root = pathlib.Path(sys.argv[1])
here = pathlib.Path(__file__).parent

def replace(path, old, new, count=1):
    text = path.read_text()
    assert text.count(old) == count, f"Wails source changed: {path}: {old[:80]!r}"
    path.write_text(text.replace(old, new))

for path in root.rglob('*gtk3.go'):
    text = path.read_text()
    path.write_text(text.replace('webkit2gtk-4.1', 'webkit2gtk-4.0').replace('libsoup-3.0', 'libsoup-2.4'))

webkit = root / 'internal/assetserver/webview/webkit_linux_gtk3.go'
replace(webkit, 'const Webkit2MinMinorVersion = 40', 'const Webkit2MinMinorVersion = 38')
text = webkit.read_text()
text, count = re.subn(r'func webkit_uri_scheme_request_get_http_body\(.*?\n\}', '', text, count=1, flags=re.S)
assert count == 1
webkit.write_text(text)

request = root / 'internal/assetserver/webview/request_linux_gtk3.go'
replace(request, '"io"', '"io"\n\t"bytes"\n\t"encoding/base64"\n\t"strconv"')
replace(request, 'r.body = webkit_uri_scheme_request_get_http_body(r.req)', '''headers, err := r.Header()
    if err != nil { return nil, err }
    encoded := headers.Get("X-Uniterm-Legacy-Body")
    if encoded == "" { r.body = http.NoBody; return r.body, nil }
    data, err := base64.StdEncoding.DecodeString(encoded)
    if err != nil { return nil, err }
    headers.Del("X-Uniterm-Legacy-Body")
    headers.Set("Content-Length", strconv.Itoa(len(data)))
    r.body = io.NopCloser(bytes.NewReader(data))''')

app = root / 'pkg/application/linux_cgo_gtk3.go'
# Both callers ignore the result, run in the main world and pass no callback.
# run_javascript is the equivalent older API, available in WebKit 2.38.
text = app.read_text()
pattern = r'C\.webkit_web_view_evaluate_javascript\(w\.webKitWebView\(\),\s*(value|dragOverJSBuffer),\s*C\.long\((?:len\(js\)|n)\),\s*nil,\s*emptyWorldName,\s*nil,\s*nil,\s*nil\)'
text, count = re.subn(pattern, r'C.webkit_web_view_run_javascript(w.webKitWebView(), \1, nil, nil, nil)', text)
assert count == 2, 'Wails JavaScript call sites changed'
text = text.replace('C.CString(linuxBlobBodyFetchShimJS)', 'C.CString(linuxBlobBodyFetchShimJS + unitermLegacyFetchJS)')
app.write_text(text)
shim = (here / 'legacy-fetch.js').read_text()
assert '`' not in shim
(root / 'pkg/application/uniterm_legacy_gtk3.go').write_text('//go:build linux && cgo && gtk3 && !android\n\npackage application\n\nconst unitermLegacyFetchJS = `' + shim + '`\n')
print('Applied WebKit 2.38 GTK3 compatibility patches')
