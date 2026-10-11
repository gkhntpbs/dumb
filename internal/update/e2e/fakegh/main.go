// fakegh is a local stand-in for GitHub Releases, used by
// qa-automation/scripts/update-e2e.sh to exercise a real signed self-update
// without GitHub. It generates throwaway minisign keys at run time; nothing it
// produces belongs in the repository.
//
//	fakegh keygen -dir D          write D/key.pub (public key line) and D/key.secret (id+seed, hex)
//	fakegh sign -key D/key.secret FILE   write FILE.minisig
//	fakegh serve -root R -addr-file F [-fail-first N]
//	                              serve R/<tag>/<asset> as /repos/gkhntpbs/dumb/releases on
//	                              127.0.0.1; the chosen URL is written to F. The first N
//	                              release listings answer 503.
package main

import (
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"

	"github.com/gkhntpbs/dumb/internal/update/updatetest"
)

func main() {
	log.SetFlags(0)
	log.SetPrefix("fakegh: ")
	if len(os.Args) < 2 {
		log.Fatal("usage: fakegh keygen|sign|serve [flags]")
	}
	fs := flag.NewFlagSet(os.Args[1], flag.ExitOnError)
	switch os.Args[1] {
	case "keygen":
		dir := fs.String("dir", "", "output directory")
		fs.Parse(os.Args[2:])
		s := updatetest.NewSigner()
		must(os.MkdirAll(*dir, 0o700))
		must(os.WriteFile(filepath.Join(*dir, "key.pub"), []byte(s.PublicKey()+"\n"), 0o644))
		secret := hex.EncodeToString(s.ID[:]) + hex.EncodeToString(s.Priv.Seed())
		must(os.WriteFile(filepath.Join(*dir, "key.secret"), []byte(secret+"\n"), 0o600))
	case "sign":
		keyFile := fs.String("key", "", "key.secret from keygen")
		fs.Parse(os.Args[2:])
		b, err := os.ReadFile(*keyFile)
		must(err)
		raw, err := hex.DecodeString(strings.TrimSpace(string(b)))
		must(err)
		if len(raw) != 8+32 {
			log.Fatal("bad key file")
		}
		var id [8]byte
		copy(id[:], raw[:8])
		s := updatetest.FromSeed(id, raw[8:])
		for _, f := range fs.Args() {
			msg, err := os.ReadFile(f)
			must(err)
			must(os.WriteFile(f+".minisig", s.Sign(msg, "file:"+filepath.Base(f)), 0o644))
		}
	case "serve":
		root := fs.String("root", "", "release directory: <root>/<tag>/<asset>")
		addrFile := fs.String("addr-file", "", "write the base URL here")
		failFirst := fs.Int("fail-first", 0, "answer 503 to the first N release listings")
		fs.Parse(os.Args[2:])
		serve(*root, *addrFile, int32(*failFirst))
	default:
		log.Fatalf("unknown command %q", os.Args[1])
	}
}

func must(err error) {
	if err != nil {
		log.Fatal(err)
	}
}

func serve(root, addrFile string, failFirst int32) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	must(err)
	base := "http://" + ln.Addr().String()
	var listings atomic.Int32
	http.HandleFunc("GET /repos/gkhntpbs/dumb/releases", func(w http.ResponseWriter, r *http.Request) {
		if listings.Add(1) <= failFirst {
			log.Print("releases: 503 (fail-first)")
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		type asset struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
		}
		type release struct {
			Tag        string  `json:"tag_name"`
			Prerelease bool    `json:"prerelease"`
			Assets     []asset `json:"assets"`
		}
		list := []release{}
		tags, _ := os.ReadDir(root)
		for _, t := range tags {
			if !t.IsDir() {
				continue
			}
			rel := release{Tag: t.Name(), Prerelease: strings.Contains(t.Name(), "-")}
			files, _ := os.ReadDir(filepath.Join(root, t.Name()))
			for _, f := range files {
				rel.Assets = append(rel.Assets, asset{f.Name(), base + "/download/" + t.Name() + "/" + f.Name()})
			}
			list = append(list, rel)
		}
		log.Printf("releases: %d listed", len(list))
		json.NewEncoder(w).Encode(list)
	})
	http.HandleFunc("GET /download/{tag}/{name}", func(w http.ResponseWriter, r *http.Request) {
		log.Printf("download %s/%s", r.PathValue("tag"), r.PathValue("name"))
		http.ServeFile(w, r, filepath.Join(root, filepath.Base(r.PathValue("tag")), filepath.Base(r.PathValue("name"))))
	})
	must(os.WriteFile(addrFile, []byte(base), 0o644))
	fmt.Println(base)
	must(http.Serve(ln, nil))
}
