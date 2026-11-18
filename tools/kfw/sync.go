package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"nickelsymbols/modcheck"
)

// The Kobo update API offers the newest update to a device that reports an
// older version. kfw reports a seed version for each channel:
//
//	stable   version 4.0.0: the newest 4.x (kobo-update-X.zip)
//	beta     version 6.0.0: the newest 6.x (qt6-update-X.zip)
//	preview  the newest stable version, with the header
//	         X-Kobo-Accept-Preview: true: the 4.x to 6.x upgrade that an
//	         opted-in 4.x device gets (qt5-qt6-X.zip)
//
// kfw sends one request for each device and channel, and only when the user
// starts it. Do not schedule it: the Kobo Terms of Use bar robots that
// monitor the service.
const api = "https://api.kobobooks.com/1.0/UpgradeCheck/Device"

// The API answer is untrusted. Download only from the official Kobo file host.
var (
	reURL  = regexp.MustCompile(`^https://ereaderfiles\.kobo\.com/firmwares/[A-Za-z0-9]+/[A-Za-z0-9]+/[A-Za-z0-9._-]+\.zip$`)
	reFile = regexp.MustCompile(`^(kobo-update|qt6-update|qt5-qt6)-([0-9]+\.[0-9]+\.[0-9]+)\.zip$`)
)

type offer struct {
	Model, Device, Channel, Version, File, URL string
}

var client = &http.Client{Timeout: 30 * time.Minute}

// upgradeURL asks the API what it offers a device on version ver.
func upgradeURL(model, ver string, preview bool) (string, error) {
	req, err := http.NewRequest("GET", fmt.Sprintf("%s/00000000-0000-0000-0000-%012s/kobo/%s/N0", api, model, ver), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "nickel-symbols-kfw")
	if preview {
		req.Header.Set("X-Kobo-Accept-Preview", "true")
	}
	c := &http.Client{Timeout: 30 * time.Second}
	resp, err := c.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("%s: %s", req.URL, resp.Status)
	}
	var a struct{ UpgradeURL string }
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&a); err != nil {
		return "", err
	}
	return a.UpgradeURL, nil
}

func readTSV(p string) ([][]string, error) {
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var rows [][]string
	s := bufio.NewScanner(f)
	for s.Scan() {
		l := s.Text()
		if l == "" || strings.HasPrefix(l, "#") {
			continue
		}
		rows = append(rows, strings.Split(l, "\t"))
	}
	return rows, s.Err()
}

// queryOffers asks the API about each device in devices.tsv.
func queryOffers(dir string) ([]offer, error) {
	rows, err := readTSV(filepath.Join(dir, "devices.tsv"))
	if err != nil {
		return nil, err
	}
	var out []offer
	for _, r := range rows {
		if len(r) < 2 || r[0] == "model" {
			continue
		}
		model, name := r[0], r[1]
		add := func(channel, u string) string {
			if u == "" {
				return ""
			}
			if !reURL.MatchString(u) {
				fmt.Fprintf(os.Stderr, "skip: unexpected URL from the API: %q\n", u)
				return ""
			}
			file := path.Base(u)
			ver := "-"
			if m := reFile.FindStringSubmatch(file); m != nil {
				ver = m[2]
			}
			out = append(out, offer{model, name, channel, ver, file, u})
			return ver
		}
		stable, err := upgradeURL(model, "4.0.0", false)
		if err != nil {
			return nil, err
		}
		sv := add("stable", stable)
		beta, err := upgradeURL(model, "6.0.0", false)
		if err != nil {
			return nil, err
		}
		add("beta", beta)
		if sv != "" && sv != "-" {
			prev, err := upgradeURL(model, sv, true)
			if err != nil {
				return nil, err
			}
			add("preview", prev)
		}
	}
	return out, nil
}

func writeOffers(p string, offers []offer) error {
	var b strings.Builder
	b.WriteString("model\tdevice\tchannel\tversion\tfile\turl\n")
	for _, o := range offers {
		fmt.Fprintf(&b, "%s\t%s\t%s\t%s\t%s\t%s\n", o.Model, o.Device, o.Channel, o.Version, o.File, o.URL)
	}
	return os.WriteFile(p, []byte(b.String()), 0o644)
}

func readOffers(p string) ([]offer, error) {
	rows, err := readTSV(p)
	if err != nil {
		return nil, err
	}
	var out []offer
	for _, r := range rows {
		if len(r) == 6 && r[0] != "model" {
			out = append(out, offer{r[0], r[1], r[2], r[3], r[4], r[5]})
		}
	}
	return out, nil
}

// download fetches u into dir and returns the file path. It writes to a
// .part file first, so a broken download never looks complete.
func download(u, dir string) (string, error) {
	if !reURL.MatchString(u) {
		return "", fmt.Errorf("refuse to download %q: not on ereaderfiles.kobo.com", u)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	dst := filepath.Join(dir, path.Base(u))
	if _, err := os.Stat(dst); err == nil {
		return dst, nil
	}
	resp, err := client.Get(u)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("%s: %s", u, resp.Status)
	}
	f, err := os.Create(dst + ".part")
	if err != nil {
		return "", err
	}
	n, err := io.Copy(f, resp.Body)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(dst + ".part")
		return "", err
	}
	fmt.Printf("downloaded %s (%d MB)\n", path.Base(u), n>>20)
	return dst, os.Rename(dst+".part", dst)
}

func cmdSync(args []string) (int, error) {
	fs := flag.NewFlagSet("sync", flag.ContinueOnError)
	offline := fs.Bool("offline", false, "ask Kobo nothing and download nothing; only run the checks")
	keep := fs.Bool("keep", false, "keep the downloaded firmware files in downloads/")
	if err := fs.Parse(args); err != nil {
		return 2, nil
	}
	dir, err := root()
	if err != nil {
		return 2, err
	}
	avail := filepath.Join(dir, "available.tsv")
	if !*offline {
		offers, err := queryOffers(dir)
		if err != nil {
			return 2, err
		}
		if err := writeOffers(avail, offers); err != nil {
			return 2, err
		}
		fmt.Println("Kobo offers:")
		for _, o := range offers {
			fmt.Printf("  %-18s %-8s %-11s %s\n", o.Device, o.Channel, o.Version, o.File)
		}
		// Index each new version. One version can come in several files:
		// take the smallest kind (kobo-update, then qt6-update, then qt5-qt6).
		byVer := map[string][]offer{}
		for _, o := range offers {
			if o.Version != "-" {
				byVer[o.Version] = append(byVer[o.Version], o)
			}
		}
		vers := make([]string, 0, len(byVer))
		for v := range byVer {
			vers = append(vers, v)
		}
		sort.Strings(vers)
		for _, v := range vers {
			if _, err := os.Stat(filepath.Join(dir, "firmware", v, "libs.json.gz")); err == nil {
				continue
			}
			var pick offer
			for _, kind := range []string{"kobo-update", "qt6-update", "qt5-qt6"} {
				for _, o := range byVer[v] {
					if pick.URL == "" && o.File == kind+"-"+v+".zip" {
						pick = o
					}
				}
			}
			if pick.URL == "" {
				continue
			}
			var devs []string
			for _, o := range byVer[v] {
				if !slices.Contains(devs, o.Device) {
					devs = append(devs, o.Device)
				}
			}
			fmt.Printf("New firmware %s (%s): %s\n", v, strings.Join(devs, ", "), pick.URL)
			zip, err := download(pick.URL, filepath.Join(dir, "downloads", v))
			if err != nil {
				fmt.Fprintln(os.Stderr, "download failed:", err)
				continue
			}
			if err := indexZip(dir, zip, strings.Join(devs, ", ")+" ("+pick.File+")", true); err != nil {
				fmt.Fprintf(os.Stderr, "indexing failed: %v. The file stays in %s.\n", err, filepath.Dir(zip))
				continue
			}
			if !*keep {
				os.Remove(zip)
				os.Remove(filepath.Dir(zip))
			}
		}
	}

	report := filepath.Join(dir, "report.md")
	code := modcheck.Main([]string{"-index", filepath.Join(dir, "firmware"), "-mods", filepath.Join(dir, "mods.tsv"), "-out", report})
	if code > 1 {
		return code, nil
	}
	if offers, err := readOffers(avail); err == nil && len(offers) > 0 {
		st, _ := os.Stat(avail)
		var b strings.Builder
		fmt.Fprintf(&b, "\n## Firmware that Kobo offers\n\nFrom the Kobo update API on %s. See `kfw sync` for the channels.\n\n| Device | Channel | Version | File |\n|---|---|---|---|\n", st.ModTime().UTC().Format("2006-01-02"))
		for _, o := range offers {
			fmt.Fprintf(&b, "| %s | %s | %s | %s |\n", o.Device, o.Channel, o.Version, o.File)
		}
		f, err := os.OpenFile(report, os.O_APPEND|os.O_WRONLY, 0)
		if err != nil {
			return 2, err
		}
		f.WriteString(b.String())
		f.Close()
	}
	b, _ := os.ReadFile(report)
	in := false
	for _, l := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(l, "| Firmware") {
			in = true
		}
		if in && !strings.HasPrefix(l, "|") {
			break
		}
		if in {
			fmt.Println(l)
		}
	}
	fmt.Println("Report:", report)
	return code, nil
}
