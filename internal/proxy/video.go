package proxy

import (
	"bufio"
	"errors"
	"io"
	"net/url"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/D-Modem/webone-go/internal/config"
	"github.com/D-Modem/webone-go/internal/rx"
)

func setOf(keys ...string) map[string]bool {
	m := make(map[string]bool, len(keys))
	for _, k := range keys {
		m[k] = true
	}
	return m
}

// ytdlpOptions are the yt-dlp options /!webvideo/ passes on
// (WebVideoConverter.cs).
var ytdlpOptions = setOf(
	"abort-on-error", "abort-on-unavailable-fragments", "add-chapters", "add-headers", "add-metadata",
	"age-limit", "alias", "allow-dynamic-mpd", "ap-list-mso", "ap-mso", "ap-password", "ap-username",
	"audio-format", "audio-multistreams", "audio-quality", "batch-file", "bidi-workaround",
	"break-match-filters", "break-on-existing", "break-per-input", "buffer-size", "cache-dir",
	"check-all-formats", "check-formats", "clean-info-json", "client-certificate",
	"client-certificate-key", "client-certificate-password", "color", "compat-options",
	"concat-playlist", "concurrent-fragments", "config-locations", "console-title", "continue",
	"convert-", "convert-subs", "convert-thumbnails", "cookies", "cookies-from-browser", "date",
	"dateafter", "datebefore", "default-search", "download-archive", "download-sections",
	"downloader", "downloader-args", "dump-pages", "embed-chapters", "embed-info-json",
	"embed-metadata", "embed-subs", "embed-thumbnail", "enable-file-urls", "encoding", "exec",
	"external-downloader", "external-downloader-args", "extract-audio", "extractor-args",
	"extractor-descriptions", "extractor-retries", "ffmpeg-location", "file-access-retries", "fixup",
	"flat-playlist", "force-download-archive", "force-ipv", "force-keyframes-at-cuts", "format",
	"format-sort", "format-sort-force", "fragment-retries", "geo-verification-proxy", "get-audio",
	"get-comments", "help", "hls-split-discontinuity", "hls-use-mpegts", "http-chunk-size", "ies",
	"ignore-config", "ignore-dynamic-mpd", "ignore-errors", "ignore-no-formats-error", "impersonate",
	"js-runtimes", "keep-fragments", "keep-video", "lazy-playlist", "legacy-server-connect",
	"limit-rate", "list-extractors", "list-formats", "list-impersonate-targets", "list-subs",
	"list-thumbnails", "live-from-start", "load-info-json", "mark-watched", "match-filters",
	"max-downloads", "max-filesize", "max-sleep-interval", "merge-output-format", "min-filesize",
	"min-sleep-interval", "mtime", "netrc", "netrc-cmd", "netrc-location", "newline",
	"no-abort-on-error", "no-abort-on-unavailable-fragments", "no-add-chapters", "no-add-metadata",
	"no-allow-dynamic-mpd", "no-audio-multistreams", "no-batch-file", "no-break-match-filters",
	"no-break-on-existing", "no-break-per-input", "no-cache-dir", "no-check-certificates",
	"no-check-formats", "no-clean-info-json", "no-config", "no-config-locations", "no-continue",
	"no-cookies", "no-cookies-from-browser", "no-download", "no-download-archive",
	"no-embed-chapters", "no-embed-info-json", "no-embed-metadata", "no-embed-subs",
	"no-embed-thumbnail", "no-exec", "no-flat-playlist", "no-force-keyframes-at-cuts",
	"no-force-overwrites", "no-format-sort-force", "no-get-comments", "no-hls-split-discontinuity",
	"no-hls-use-mpegts", "no-ignore-dynamic-mpd", "no-ignore-errors", "no-ignore-no-formats-error",
	"no-js-", "no-js-runtimes", "no-keep-fragments", "no-keep-video", "no-lazy-playlist",
	"no-live-from-start", "no-mark-watched", "no-match-filters", "no-mtime", "no-overwrites",
	"no-part", "no-playlist", "no-plugin-dirs", "no-post-overwrites", "no-prefer-free-formats",
	"no-progress", "no-quiet", "no-remote-components", "no-remove-chapters", "no-resize-buffer",
	"no-restrict-filenames", "no-simulate", "no-skip-unavailable-fragments", "no-split-chapters",
	"no-sponsorblock", "no-update", "no-video-multistreams", "no-wait-for-video", "no-warnings",
	"no-windows-filenames", "no-write-auto-subs", "no-write-automatic-subs", "no-write-comments",
	"no-write-description", "no-write-info-json", "no-write-playlist-metafiles", "no-write-subs",
	"no-write-thumbnail", "output", "output-na-placeholder", "parse-metadata", "part", "password",
	"paths", "playlist-items", "playlist-random", "playlist-reverse", "plugin-dirs",
	"postprocessor-args", "ppa", "prefer-free-formats", "prefer-insecure", "preset-alias", "print",
	"print-to-file", "print-traffic", "progress", "progress-delta", "progress-template", "proxy",
	"quiet", "recode-video", "remote-components", "remove-chapters", "remux-video",
	"replace-in-metadata", "resize-buffer", "restrict-filenames", "retries", "retry-sleep",
	"rm-cache-dir", "simulate", "skip-download", "skip-playlist-after-errors",
	"skip-unavailable-fragments", "sleep-interval", "sleep-requests", "sleep-subtitles",
	"socket-timeout", "source-address", "split-chapters", "sponsorblock-api",
	"sponsorblock-chapter-title", "sponsorblock-mark", "sponsorblock-remove", "sub-", "sub-format",
	"sub-langs", "throttled-rate", "trim-filenames", "twofactor", "use-extractors",
	"use-postprocessor", "username", "verbose", "version", "video-multistreams", "video-password",
	"wait-for-video", "windows-filenames", "xattrs", "xff", "yes-playlist", "a", "audio", "based",
	"c", "compatible", "dl", "dlc", "dlp-ejs", "encode", "extracted", "factor", "filler", "free",
	"generated", "generic", "h", "i", "inf", "k", "language", "letter", "live", "modified", "n", "o",
	"only", "preview", "processed", "q", "range", "restricted", "restriction", "sensitive",
	"separated", "side", "specific", "system", "title", "tty", "v", "w", "x",
)

// ytdlpUnsafe are yt-dlp options that read or write files on the proxy or
// run programs. WebOne passes them from the URL; this port takes them only
// from [WebVideoOptions] in the configuration.
var ytdlpUnsafe = setOf(
	"a", "batch-file", "cache-dir", "client-certificate", "client-certificate-key", "client-certificate-password",
	"config-locations", "cookies", "cookies-from-browser", "download-archive", "downloader", "downloader-args",
	"enable-file-urls", "exec", "external-downloader", "external-downloader-args", "ffmpeg-location",
	"load-info-json", "netrc", "netrc-cmd", "netrc-location", "o", "output", "p", "paths", "plugin-dirs",
	"postprocessor-args", "ppa", "print-to-file", "rm-cache-dir", "use-postprocessor",
)

// ffmpegOptions are the ffmpeg options /!webvideo/ passes on.
var ffmpegOptions = setOf(
	"loglevel", "max_alloc", "filter_threads", "filter_complex_threads", "stats", "max_error_rate",
	"bits_per_raw_sample", "vol", "codec", "pre", "t", "to", "fs", "ss", "sseof", "seek_timestamp",
	"timestamp", "metadata", "program", "target", "apad", "frames", "filter_script", "reinit_filter",
	"discard", "disposition", "vframes", "r", "s", "aspect", "vn", "vcodec", "timecode", "pass", "ab", "b",
	"dn", "aframes", "aq", "ar", "ac", "an", "acodec", "sn", "scodec", "stag", "fix_sub_duration",
	"canvas_size", "spre", "f",
)

var videoFormats = map[string][2]string{
	"avi":        {"video/msvideo", "onlinevideo.avi"},
	"mpeg1video": {"video/mpeg", "onlinevideo.mpg"},
	"mpeg2video": {"video/mpeg", "onlinevideo.mpg"},
	"mpeg4":      {"video/mp4", "onlinevideo.mp4"},
	"mpegts":     {"video/mp2t", "onlinevideo.mts"},
	"asf":        {"video/x-ms-asf", "onlinevideo.asf"},
	"asf_stream": {"video/x-ms-asf", "onlinevideo.asf"},
	"wmv":        {"video/x-ms-asf", "onlinevideo.asf"},
	"mov":        {"video/qucktime", "onlinevideo.mov"},
	"ogg":        {"video/ogg", "onlinevideo.ogg"},
	"webm":       {"video/webm", "onlinevideo.webm"},
	"swf":        {"application/x-shockwave-flash", "onlinevideo.swf"},
	"rm":         {"application/vnd.rn-realmedia", "onlinevideo.rm"},
	"3gp":        {"video/3gpp", "onlinevideo.3gp"},
}

// videoEnabled: [WebVideoOptions] Enable is on.
func (t *transit) videoEnabled() bool {
	v, ok := t.cfg.WebVideoOptions["Enable"]
	return ok && config.ToBool(v)
}

func (t *transit) videoOption(name, def string) string {
	if v, ok := t.cfg.WebVideoOptions[name]; ok && v != "" {
		return v
	}
	return def
}

// queryArgs is the query string as ordered pairs (later duplicates win).
func queryArgs(q string) *config.Vars {
	v := config.NewVars()
	vals, _ := url.ParseQuery(q)
	keys := make([]string, 0, len(vals))
	for k := range vals {
		keys = append(keys, k)
	}
	// keep the order of the query string
	sort.SliceStable(keys, func(i, j int) bool { return strings.Index(q, keys[i]+"=") < strings.Index(q, keys[j]+"=") })
	for _, k := range keys {
		v.Set(k, vals.Get(k))
	}
	return v
}

// webVideo is /!webvideo/: an online video through yt-dlp (and ffmpeg),
// in a format the old computer can play (WebVideoConverter.cs).
func (t *transit) webVideo(query string) {
	if !t.videoEnabled() {
		t.sendRedirect("/norovp.htm", "Video Converting and ROVP are disabled on this server.")
		return
	}
	args := queryArgs(query)
	if _, ok := args.Get("url"); !ok {
		help := "<p>WebOne can help download videos from popular sites in preferred format.</p>" +
			"<p>To download a video, go to <b><a href=\"/!player/\">Online Video Player</a></b>, enter URL of the video, " +
			"choose container and codecs valid for your system, and then select <b>file</b> or <b>link</b> option.</p>" +
			"<p>If you choose <b>file</b> option, the video file will start download automatically. " +
			"If you choose <b>link</b> option, you will get a link, which can be copied to multimedia player program.</p>" +
			"<p>Manual use parameters:<ul>" +
			"<li><b>url</b> - Address of the video (e.g. https://www.youtube.com/watch?v=fPnO26CwqYU or similar)</li>" +
			"<li><b>f</b> - Target format of the file (e.g. avi)</li>" +
			"<li><b>vcodec</b> - Codec for video (e.g. mpeg4)</li>" +
			"<li><b>acodec</b> - Codec for audio (e.g. mp3)</li>" +
			"<li><b>content-type</b> - override MIME content type for the file (optional).</li>" +
			"<li>Also you can use many <i>" + t.videoOption("YouTubeDlApp", "yt-dlp") +
			"</i> and <i>" + t.videoOption("FFmpegApp", "ffmpeg") +
			"</i> options like <b>aspect</b>, <b>b</b>, <b>no-mark-watched</b> and other.</li>" +
			"<li>Default parameter values are stored in configuration file.</li></ul></p>"
		t.sendInfoPage("Online video converter", "Web video converting", help, 200)
		return
	}
	v, err := t.convertVideo(args)
	if err != nil {
		t.log.Printf("Cannot convert video: %v", err)
		msg := "<p>" + err.Error() + "</p>" +
			"<p>Make sure that parameters are correct, and both <i>yt-dlp</i> and <i>ffmpeg</i> are properly installed on the server.</p>"
		t.sendInfoPage("Online video converter", "Web video converting", msg, 200)
		return
	}
	t.resp.AddHeader("Content-Disposition", `attachment; filename="`+v.fileName+`"`)
	t.sendStream(v.stream, v.contentType, -1, 200)
}

type webVideo struct {
	stream      io.ReadCloser
	contentType string
	fileName    string
}

func (t *transit) convertVideo(args *config.Vars) (*webVideo, error) {
	fromURL := map[string]bool{}
	for _, k := range args.Keys() {
		fromURL[k] = true
	}
	opts := make([]string, 0, len(t.cfg.WebVideoOptions))
	for k := range t.cfg.WebVideoOptions {
		opts = append(opts, k)
	}
	sort.Strings(opts)
	for _, k := range opts {
		args.TryAdd(k, t.cfg.WebVideoOptions[k])
	}
	mime, name := "application/octet-stream", "video.avi"
	if f, ok := args.Get("f"); ok {
		if p, ok := videoFormats[f]; ok {
			mime, name = p[0], p[1]
		} else {
			name = "onlinevideo." + f
		}
	}
	for _, k := range []string{"j", "J", "dump-json", "dump-single-json", "print-json"} {
		if _, ok := args.Get(k); ok {
			mime, name = "application/json", "metadata.json"
		}
	}
	args.TryAdd("content-type", mime)
	args.TryAdd("filename", name)

	var ytArgs, ffArgs []string
	add := func(list *[]string, prefix, k, v string) {
		*list = append(*list, prefix+k)
		if v != "" {
			*list = append(*list, v)
		}
	}
	useFFmpeg, json := true, false
	vcodec, _ := args.Get("vcodec")
	acodec, _ := args.Get("acodec")
	for _, k := range args.Keys() {
		v := args.Value(k)
		if (strings.HasPrefix(k, "vf") && vcodec != "copy") || (strings.HasPrefix(k, "af") && acodec != "copy") || strings.HasPrefix(k, "filter") {
			add(&ffArgs, "-", k, v)
			continue
		}
		lk := strings.ToLower(k)
		switch {
		case lk == "enable":
			if !config.ToBool(v) {
				return nil, errors.New("This feature is disabled by administrator.")
			}
		case lk == "url" || lk == "content-type" || lk == "filename" || lk == "prefer" || lk == "gui" || lk == "youtubedlapp" || lk == "ffmpegapp":
		case lk == "noffmpeg":
			useFFmpeg = !config.ToBool(v)
		case lk == "j" || lk == "dump-json" || lk == "dump-single-json" || lk == "print-json":
			add(&ytArgs, "--", k, v)
			useFFmpeg, json = false, true
		case ytdlpOptions[lk]:
			if ytdlpUnsafe[lk] && fromURL[k] {
				t.log.Printf(" Refused argument from URL: %s", k)
				continue
			}
			add(&ytArgs, "--", k, v)
		case ffmpegOptions[lk]:
			add(&ffArgs, "-", k, v)
		default:
			t.log.Printf(" Unsupported argument: %s", k)
		}
	}
	video := args.Value("url")
	if strings.HasPrefix(video, "-") {
		return nil, errors.New("Internet video address is not correct.")
	}
	yt := exec.Command(t.videoOption("YouTubeDlApp", "yt-dlp"), append(append([]string{video}, ytArgs...), "-o", "-")...)
	ytOut, err := yt.StdoutPipe()
	if err != nil {
		return nil, err
	}
	ytErr, err := yt.StderrPipe()
	if err != nil {
		return nil, err
	}
	var ff *exec.Cmd
	var out io.ReadCloser = ytOut
	if useFFmpeg {
		ff = exec.Command(t.videoOption("FFmpegApp", "ffmpeg"), append(append([]string{"-i", "pipe:"}, ffArgs...), "pipe:")...)
		ff.Stdin = ytOut
		if out, err = ff.StdoutPipe(); err != nil {
			return nil, err
		}
		t.log.Printf(" Video convert: %s %s | %s %s", yt.Path, strings.Join(yt.Args[1:], " "), ff.Path, strings.Join(ff.Args[1:], " "))
	} else {
		t.log.Printf(" Video convert: %s %s", yt.Path, strings.Join(yt.Args[1:], " "))
	}
	if err := yt.Start(); err != nil {
		return nil, err
	}
	if ff != nil {
		if err := ff.Start(); err != nil {
			yt.Process.Kill()
			yt.Wait()
			return nil, err
		}
	}

	// yt-dlp's messages: an ERROR line means there is no video
	var mu sync.Mutex
	failure := ""
	failed := make(chan struct{}, 1)
	go func() {
		sc := bufio.NewScanner(ytErr)
		for sc.Scan() {
			l := sc.Text()
			switch {
			case strings.HasPrefix(l, "ERROR:"):
				mu.Lock()
				failure = "Online video failed to download: " + strings.TrimSpace(l[6:])
				mu.Unlock()
				t.log.Printf(" yt-dlp: %s", l)
				select {
				case failed <- struct{}{}:
				default:
				}
			case strings.HasPrefix(l, "WARNING:"):
				t.log.Printf(" yt-dlp: %s", l)
			}
		}
	}()
	pr := &videoReader{br: bufio.NewReaderSize(out, 64*1024), procs: []*exec.Cmd{yt, ff}}
	ready := make(chan error, 1)
	go func() { _, err := pr.br.Peek(1); ready <- err }()
	select {
	case err = <-ready:
	case <-failed:
		err = errors.New("failed")
	case <-time.After(10 * time.Minute):
		err = errors.New("Online video download has timed out.")
	}
	if err != nil {
		mu.Lock()
		msg := failure
		mu.Unlock()
		if msg == "" {
			msg = "Online video is not available: " + err.Error()
		}
		pr.Close()
		return nil, errors.New(msg)
	}
	ct := args.Value("content-type")
	if json {
		ct = "application/json"
	}
	return &webVideo{stream: pr, contentType: ct, fileName: args.Value("filename")}, nil
}

// videoReader is the converted video; closing it stops the programs.
type videoReader struct {
	br    *bufio.Reader
	procs []*exec.Cmd
	once  sync.Once
}

func (v *videoReader) Read(p []byte) (int, error) { return v.br.Read(p) }

func (v *videoReader) Close() error {
	v.once.Do(func() {
		for _, c := range v.procs {
			if c != nil && c.Process != nil {
				c.Process.Kill()
			}
		}
		for _, c := range v.procs {
			if c != nil && c.Process != nil {
				c.Wait()
			}
		}
	})
	return nil
}

// videoPlayerPage is the Retro Online Video Player, /!player/
// (WebVideoPlayer.cs).
func (t *transit) videoPlayerPage(query string) *infoPage {
	p := newInfoPage("Retro online video player", "", "", 200)
	p.ShowFooter = false
	p.HTMLHeaders = "<style type='text/css'>html, body { border-style: none; } </style>"
	args := queryArgs(query)
	pass := func(base string) string {
		for _, k := range args.Keys() {
			if k != "type" {
				base += k + "=" + url.QueryEscape(args.Value(k)) + "&"
			}
		}
		return base
	}
	videoURL := pass("/!webvideo/?")
	typ := args.Value("type")
	profile := args.Value("profile")
	if strings.TrimSpace(typ) == "" && strings.TrimSpace(profile) != "" {
		if prof, ok := t.cfg.WebVideoProfiles[profile]; ok && len(prof) > 1 {
			p.Content = "Loading the profile..."
			p.Status = 302
			p.Headers.Add("Location", pass("/!player/?")+prof[1])
			return p
		}
	}
	if !t.videoEnabled() {
		p.Content = "It's disabled."
		p.Status = 302
		p.Headers.Add("Location", "/norovp.htm")
		return p
	}
	if strings.TrimSpace(typ) == "" {
		typ = "splash"
	}
	switch typ {
	case "link":
		p.Content = "<center><big><a href='" + videoURL + "'>Download the video</a></big></center>"
		if _, ok := args.Get("f"); !ok {
			p.Content += "<center><p>Or select format, codecs and convert the video online.</p></center>"
		}
		p.Title = "Video player - link only"
	case "file":
		p.Content = "<center>Please wait up to 30 sec.<br>If nothing appear, <a href='" + videoURL + "'>click here</a> to download manually.</center>"
		p.Headers.Add("Refresh", "0; url="+videoURL)
		p.Title = "Video player - FILE REDIRECT"
	default:
		p.Status = 302
		loc := "/rovp-" + typ + ".htm"
		if videoURL != "/!webvideo/?" {
			loc += "?VideoUrl=" + url.QueryEscape(videoURL)
		}
		if typ == "html5" {
			loc += "&JsonUrl=" + url.QueryEscape("/!webvideo/?&dump-json=&url="+args.Value("url")+"&no-warnings=&quiet=")
		}
		p.Headers.Add("Location", loc)
	}
	return p
}

// redirectorPage is /!redirect/?RegExMask=...&RegExGroup=n&OriginalString=...
// (RedirectorInfoPage.cs).
func (t *transit) redirectorPage(query string) *infoPage {
	p := newInfoPage("WebOne: Redirection", "Redirection", "", 200)
	p.Content = ""
	args, _ := url.ParseQuery(query)
	mask := args.Get("RegExMask")
	orig := args.Get("OriginalString")
	group := 0
	if g := args.Get("RegExGroup"); g != "" {
		for _, r := range g {
			if r < '0' || r > '9' {
				group = 0
				break
			}
			group = group*10 + int(r-'0')
		}
	}
	fail := func(msg string) *infoPage {
		p.Content += "<p><b>Error: " + msg + "</b></p>"
		return p
	}
	if strings.TrimSpace(mask) == "" {
		return fail("RegExMask is empty.")
	}
	if strings.TrimSpace(orig) == "" {
		return fail("OriginalString is empty.")
	}
	m, ok := rx.FindGroupsOK(mask, orig)
	if m == nil {
		return p
	}
	if group >= len(m) {
		return fail("RegExGroup is more than " + strconv.Itoa(len(m)) + ".")
	}
	u := ""
	if ok[group] {
		u = m[group]
	}
	p.Content = "<p>It will be better to open <a href='" + u + "'>" + u + "</a> instead of this stub.</p>"
	p.Status = 302
	p.Headers.Add("Location", u)
	return p
}
