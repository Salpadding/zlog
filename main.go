package zlog

import (
	"bytes"
	"io"
	"net/http"
	"os"

	caddy "github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/caddyconfig/caddyfile"
	"github.com/caddyserver/caddy/v2/caddyconfig/httpcaddyfile"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
)

const (
	MaxBufferSize = 1024 * 1024
)

func init() {
	caddy.RegisterModule(&ZLog{})
	httpcaddyfile.RegisterHandlerDirective("zlog", parseCaddyfile)
}

type ZLog struct {
}

func (z *ZLog) CaddyModule() caddy.ModuleInfo {
	return caddy.ModuleInfo{
		ID:  "zlog",
		New: func() caddy.Module { return new(ZLog) },
	}
}

func (z *ZLog) UnmarshalCaddyfile(d *caddyfile.Dispenser) error {
	return nil
}

type proxyWriter struct {
	http.ResponseWriter
	buf bytes.Buffer
}

func (p *proxyWriter) Write(data []byte) (code int, err error) {
	code, err = p.ResponseWriter.Write(data)
	if p.buf.Len() >= MaxBufferSize {
		return
	}
	for i := range data {
		// 非 ascii 字符
		if data[i] > 127 {
			return
		}
	}
	p.buf.Write(data)
	return
}

func (z *ZLog) ServeHTTP(w http.ResponseWriter, r *http.Request, next caddyhttp.Handler) (err error) {
	writer := proxyWriter{
		ResponseWriter: w,
	}
	err = next.ServeHTTP(&writer, r)
	if writer.buf.Len() > 0 {
		io.Copy(os.Stdout, &writer.buf)
	}
	return
}

// parseCaddyfile unmarshals tokens from h into a new Middleware.
func parseCaddyfile(h httpcaddyfile.Helper) (caddyhttp.MiddlewareHandler, error) {
	var zlog ZLog
	err := zlog.UnmarshalCaddyfile(h.Dispenser)
	return &zlog, err
}
