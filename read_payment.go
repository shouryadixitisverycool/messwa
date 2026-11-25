package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"messwa/internal/payshot"
)

//go:embed read_payment.html
var readPaymentPage []byte

// paymentRead is one screenshot's result as read-payment prints it.
type paymentRead struct {
	File  string `json:"file,omitempty"`
	Model string `json:"model"`
	Took  string `json:"took"`
	Paise int64  `json:"amount_paise"`
	payshot.Receipt
	PayeeOK *bool `json:"payee_matches,omitempty"`
}

func readOne(ctx context.Context, r *payshot.Reader, img []byte, mime, upi, name string) (paymentRead, error) {
	start := time.Now()
	rc, model, err := r.Read(ctx, img, mime)
	if err != nil {
		return paymentRead{}, err
	}
	res := paymentRead{Model: model, Took: time.Since(start).Round(10 * time.Millisecond).String(), Paise: rc.AmountPaise, Receipt: rc}
	if upi != "" || name != "" {
		// The rule sales use; see payshot.PaidTo.
		ok := payshot.PaidTo(rc, upi, name)
		res.PayeeOK = &ok
	}
	return res, nil
}

// readPayment reads payment screenshots with the vision model and prints
// what it saw, to try the reader before trusting it with sales. With
// -serve it serves a page to upload screenshots instead. It needs no
// config file; the key comes from GEMINI_API_KEY.
func readPayment(ctx context.Context, out io.Writer, args []string) error {
	fs := flag.NewFlagSet("read-payment", flag.ContinueOnError)
	models := fs.String("models", strings.Join(payshot.DefaultModels, ","), "Gemini models to try in order, comma-separated")
	upi := fs.String("upi", "", "your UPI ID, to check the screenshot paid it")
	serve := fs.Bool("serve", false, "serve an upload page instead of reading files")
	listen := fs.String("listen", "127.0.0.1:8099", "with -serve, addresses to listen on, comma-separated")
	name := fs.String("name", "", "your banking name, to check the screenshot paid you (see [sell] payee_name)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	r := &payshot.Reader{Key: os.Getenv("GEMINI_API_KEY"), Models: strings.Split(*models, ",")}
	if r.Key == "" {
		return errors.New("set GEMINI_API_KEY")
	}
	if *serve {
		return servePaymentReader(ctx, r, *upi, *name, strings.Split(*listen, ","))
	}
	if fs.NArg() == 0 {
		return errors.New("usage: messwa read-payment [-upi ID] [-name NAME] [-models M,...] IMAGE... | -serve [-listen ADDR,...]")
	}
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	for _, path := range fs.Args() {
		img, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		res, err := readOne(ctx, r, img, "", *upi, *name)
		if err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		res.File = path
		if err := enc.Encode(res); err != nil {
			return err
		}
	}
	return nil
}

func servePaymentReader(ctx context.Context, r *payshot.Reader, defaultUPI, defaultName string, addrs []string) error {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(readPaymentPage)
	})
	mux.HandleFunc("GET /config", func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"upi": defaultUPI, "name": defaultName, "models": r.Models})
	})
	mux.HandleFunc("POST /read", func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		req.Body = http.MaxBytesReader(w, req.Body, 10<<20)
		f, hdr, err := req.FormFile("image")
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]string{"error": "send an image under 10 MB: " + err.Error()})
			return
		}
		defer f.Close()
		img, err := io.ReadAll(f)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}
		res, err := readOne(req.Context(), r, img, hdr.Header.Get("Content-Type"), strings.TrimSpace(req.FormValue("upi")), strings.TrimSpace(req.FormValue("name")))
		if err != nil {
			w.WriteHeader(http.StatusBadGateway)
			json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}
		res.File = hdr.Filename
		enc := json.NewEncoder(w)
		enc.SetEscapeHTML(false)
		enc.Encode(res)
	})
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	errc := make(chan error, len(addrs))
	for _, a := range addrs {
		ln, err := net.Listen("tcp", strings.TrimSpace(a))
		if err != nil {
			srv.Close()
			return err
		}
		fmt.Fprintf(os.Stderr, "read-payment: serving http://%s/\n", ln.Addr())
		go func() { errc <- srv.Serve(ln) }()
	}
	select {
	case <-ctx.Done():
		srv.Close()
		return nil
	case err := <-errc:
		srv.Close()
		return err
	}
}
