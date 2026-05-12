package mcp

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	qbt "github.com/autobrr/go-qbittorrent"

	"github.com/wyvernzora/qbittorrent-mcp/internal/savepath"
)

// fixture6Downloads is the upstream /api/v2/torrents/info payload used by
// most list_downloads test cases. Fields populated:
//
//	aaa anime-show-01  downloading  tags=tvdb:12345,weekly  eta=1234
//	bbb anime-show-02  uploading    tags=tvdb:67890,complete progress=1.0
//	ccc movie          stalledDL    tags=weekly             eta=8640000 (sentinel → -1)
//	ddd archive-thing  pausedUP     tags=""
//	eee just-added     metaDL       tags=""                  (normalizes to downloading)
//	fff other-anime    downloading  tags=tvdb:11111
const fixture6Downloads = `[
  {"hash":"aaa","name":"anime-show-01","state":"downloading","progress":0.42,"size":100,"dlspeed":500,"upspeed":0,"eta":1234,"ratio":0.0,"tags":"tvdb:12345,weekly","added_on":1000,"save_path":"/data/anime","completion_on":0,"uploaded":0,"downloaded":42,"num_complete":50,"num_seeds":5,"num_leechs":3,"trackers_count":2},
  {"hash":"bbb","name":"anime-show-02","state":"uploading","progress":1.0,"size":200,"dlspeed":0,"upspeed":1000,"eta":0,"ratio":2.5,"tags":"tvdb:67890,complete","added_on":900,"save_path":"/data/anime","completion_on":950,"uploaded":500,"downloaded":200,"num_complete":100,"num_seeds":10,"num_leechs":0,"trackers_count":3},
  {"hash":"ccc","name":"movie","state":"stalledDL","progress":0.5,"size":300,"dlspeed":0,"upspeed":0,"eta":8640000,"ratio":0.1,"tags":"weekly","added_on":800,"save_path":"/data/movies","trackers_count":1},
  {"hash":"ddd","name":"archive-thing","state":"pausedUP","progress":1.0,"size":400,"dlspeed":0,"upspeed":0,"eta":0,"ratio":1.0,"tags":"","added_on":700},
  {"hash":"eee","name":"just-added","state":"metaDL","progress":0.0,"size":0,"dlspeed":0,"upspeed":0,"eta":0,"ratio":0.0,"tags":"","added_on":600},
  {"hash":"fff","name":"other-anime","state":"downloading","progress":0.1,"size":150,"dlspeed":100,"upspeed":0,"eta":1500,"ratio":0.0,"tags":"tvdb:11111","added_on":500}
]`

// fixture1Download is a single-download payload used by tests that
// exercise the rich projection set (auto_tmm, force_start, private etc.)
// on a single hash.
const fixture1Download = `[{
  "hash":"aaa","name":"anime-show-01","state":"downloading","progress":0.42,
  "size":100,"total_size":120,"dlspeed":500,"upspeed":0,"eta":1234,"ratio":0.5,
  "tags":"tvdb:12345,weekly","added_on":1000,"last_activity":1500,
  "save_path":"/data/anime","content_path":"/data/anime/show.mkv","download_path":"/data/incoming","magnet_uri":"magnet:?xt=urn:btih:aaa",
  "completion_on":0,"uploaded":7,"downloaded":42,
  "num_complete":50,"num_incomplete":3,"num_seeds":5,"num_leechs":2,"trackers_count":2,
  "auto_tmm":true,"seq_dl":false,"force_start":false,"super_seeding":false,"f_l_piece_prio":true,
  "ratio_limit":2.0,"seeding_time":100,"seeding_time_limit":3600,"private":true
}]`

const fixtureTrackers = `[
  {"url":"http://tracker1.example/announce","status":2,"num_peers":7,"num_seeds":50,"num_leeches":3,"num_downloaded":0,"msg":""},
  {"url":"udp://tracker2.example/announce","status":4,"num_peers":0,"num_seeds":0,"num_leeches":0,"num_downloaded":0,"msg":"connection refused"}
]`

const fixtureFiles = `[
  {"index":0,"name":"show/episode-01.mkv","size":1000,"progress":0.5,"priority":1,"availability":1.0,"piece_range":[0,99],"is_seed":false},
  {"index":1,"name":"show/episode-02.mkv","size":2000,"progress":1.0,"priority":1,"availability":1.0,"piece_range":[100,199],"is_seed":false}
]`

type capturedReq struct {
	Method   string
	Path     string
	Query    url.Values
	PostForm url.Values
}

func captureReq(cap *capturedReq, r *http.Request) {
	cap.Method = r.Method
	cap.Path = r.URL.Path
	cap.Query = r.URL.Query()
	if r.Method == http.MethodPost {
		_ = r.ParseForm()
		cap.PostForm = r.PostForm
	}
}

func newQbitMock(t *testing.T, body string) (*qbt.Client, *capturedReq) {
	t.Helper()
	cap := &capturedReq{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captureReq(cap, r)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return qbt.NewClient(qbt.Config{Host: srv.URL, Timeout: 2, RetryAttempts: 1, RetryDelay: 1}), cap
}

func newQbitMockStatus(t *testing.T, status int) *qbt.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
	}))
	t.Cleanup(srv.Close)
	return qbt.NewClient(qbt.Config{Host: srv.URL, Timeout: 2, RetryAttempts: 1, RetryDelay: 1})
}

type mockRoute struct {
	status int
	body   string
}

func newQbitMockRoutes(t *testing.T, routes map[string]mockRoute) (client *qbt.Client, captured map[string]*capturedReq) {
	t.Helper()
	captured = make(map[string]*capturedReq, len(routes))
	for k := range routes {
		captured[k] = &capturedReq{}
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		route, ok := routes[r.URL.Path]
		if !ok {
			t.Errorf("unrouted request to %q", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		captureReq(captured[r.URL.Path], r)
		if route.status != 0 && route.status != http.StatusOK {
			w.WriteHeader(route.status)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(route.body))
	}))
	t.Cleanup(srv.Close)
	return qbt.NewClient(qbt.Config{Host: srv.URL, Timeout: 2, RetryAttempts: 1, RetryDelay: 1}), captured
}

func mustResolver(t *testing.T, spec string) *savepath.Resolver {
	t.Helper()
	r, err := savepath.Parse(spec)
	if err != nil {
		t.Fatalf("savepath.Parse(%q): %v", spec, err)
	}
	return r
}

func callSearchDownloads(t *testing.T, client *qbt.Client, in SearchDownloadsInput) (SearchDownloadsOutput, *ToolError) {
	t.Helper()
	return callSearchDownloadsWithResolver(t, client, mustResolver(t, ""), in)
}

// callSearchDownloadsWithResolver lets tests that exercise the
// destination-alias prefix on save_path / content_path / download_path
// supply a populated resolver. Most tests don't care and use the empty
// default via callSearchDownloads.
func callSearchDownloadsWithResolver(t *testing.T, client *qbt.Client, resolver *savepath.Resolver, in SearchDownloadsInput) (SearchDownloadsOutput, *ToolError) {
	t.Helper()
	h := searchDownloadsHandler(client, resolver)
	return h(context.Background(), in)
}

func callAddDownload(t *testing.T, client *qbt.Client, resolver *savepath.Resolver, in AddDownloadInput) (AddDownloadOutput, *ToolError) {
	t.Helper()
	h := addDownloadHandler(client, resolver, discardLogger())
	return h(context.Background(), in)
}

func callRemoveDownloads(t *testing.T, client *qbt.Client, in RemoveDownloadsInput) (AffectedOutput, *ToolError) {
	t.Helper()
	h := removeDownloadsHandler(client, discardLogger())
	return h(context.Background(), in)
}

func bufferedLogger() (*bytes.Buffer, *slog.Logger) {
	buf := &bytes.Buffer{}
	logger := slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	return buf, logger
}

// --- list_downloads ---

func TestSearchDownloads_DefaultReturnsAll(t *testing.T) {
	client, _ := newQbitMock(t, fixture6Downloads)
	out, terr := callSearchDownloads(t, client, SearchDownloadsInput{})
	if terr != nil {
		t.Fatalf("unexpected error: %+v", terr)
	}
	if out.Count != 6 {
		t.Errorf("count = %d, want 6", out.Count)
	}
	if out.HasMore {
		t.Error("has_more should be false")
	}
	if len(out.Downloads) != 6 {
		t.Errorf("downloads len = %d, want 6", len(out.Downloads))
	}
}

func TestSearchDownloads_LeanProjectionOmitsOptIn(t *testing.T) {
	client, _ := newQbitMock(t, fixture6Downloads)
	out, _ := callSearchDownloads(t, client, SearchDownloadsInput{})
	if len(out.Downloads) == 0 {
		t.Fatal("expected results")
	}
	first := out.Downloads[0]
	if first.SavePath != "" {
		t.Error("SavePath should be empty in lean projection")
	}
	if first.SeedsComplete != 0 {
		t.Error("SeedsComplete should be zero in lean projection")
	}
	if first.TrackerCount != 0 {
		t.Error("TrackerCount should be zero in lean projection")
	}
	if first.TotalUploaded != 0 {
		t.Error("TotalUploaded should be zero in lean projection")
	}
}

func TestSearchDownloads_SavePathPrefixedToAlias(t *testing.T) {
	// fixture6Downloads has save_path=/data/anime on aaa/bbb and
	// /data/movies on ccc. With aliases configured for both, the
	// projection should rewrite save_path as "anime" / "movies"
	// (exact-root match, no relpath suffix).
	client, _ := newQbitMock(t, fixture6Downloads)
	resolver := mustResolver(t, "anime=/data/anime,movies=/data/movies")
	out, terr := callSearchDownloadsWithResolver(t, client, resolver, SearchDownloadsInput{
		IncludeFields: []string{"save_path"},
	})
	if terr != nil {
		t.Fatalf("err = %+v", terr)
	}
	got := map[string]string{}
	for _, d := range out.Downloads {
		got[d.Hash] = d.SavePath
	}
	for hash, want := range map[string]string{"aaa": "anime", "bbb": "anime", "ccc": "movies"} {
		if got[hash] != want {
			t.Errorf("download %s save_path = %q, want %q", hash, got[hash], want)
		}
	}
}

func TestSearchDownloads_SavePathFallsThroughToUnspecified(t *testing.T) {
	// No resolver alias covers /data/anime. save_path must surface as
	// the "unspecified:<...>" sentinel form so every output path
	// parses as "<prefix>:<rest>" — there is no raw-absolute escape
	// hatch on the wire.
	client, _ := newQbitMock(t, fixture6Downloads)
	out, _ := callSearchDownloads(t, client, SearchDownloadsInput{
		IncludeFields: []string{"save_path"},
	})
	var aaa *Download
	for i := range out.Downloads {
		if out.Downloads[i].Hash == "aaa" {
			aaa = &out.Downloads[i]
			break
		}
	}
	if aaa == nil {
		t.Fatal("download aaa not found in fixture output")
	}
	if aaa.SavePath != "unspecified:data/anime" {
		t.Errorf("save_path = %q, want 'unspecified:data/anime'", aaa.SavePath)
	}
}

func TestSearchDownloads_ContentPathPrefixedToAliasWithRelpath(t *testing.T) {
	// fixture1Download has content_path=/data/anime/show.mkv. With an
	// "anime" alias rooted at /data/anime, content_path should resolve
	// to "anime:show.mkv".
	client, _ := newQbitMockRoutes(t, map[string]mockRoute{
		"/api/v2/torrents/info": {body: fixture1Download},
	})
	resolver := mustResolver(t, "anime=/data/anime")
	out, terr := callSearchDownloadsWithResolver(t, client, resolver, SearchDownloadsInput{
		Hashes:        []string{"aaa"},
		IncludeFields: []string{"content_path", "download_path"},
	})
	if terr != nil {
		t.Fatalf("err = %+v", terr)
	}
	d := out.Downloads[0]
	if d.ContentPath != "anime:show.mkv" {
		t.Errorf("content_path = %q, want 'anime:show.mkv'", d.ContentPath)
	}
	// download_path=/data/incoming has no matching alias; falls
	// through to the unspecified sentinel.
	if d.DownloadPath != "unspecified:data/incoming" {
		t.Errorf("download_path = %q, want 'unspecified:data/incoming'", d.DownloadPath)
	}
}

func TestSearchDownloads_IncludeFieldsPopulatesOnlyRequested(t *testing.T) {
	client, _ := newQbitMock(t, fixture6Downloads)
	out, _ := callSearchDownloads(t, client, SearchDownloadsInput{
		IncludeFields: []string{"save_path", "seeds"},
	})
	first := out.Downloads[0]
	if first.SavePath == "" && first.SeedsComplete == 0 {
		t.Fatal("expected SavePath or SeedsComplete populated")
	}
	if first.TrackerCount != 0 {
		t.Error("TrackerCount should remain zero (not requested)")
	}
	if first.MagnetURI != "" {
		t.Error("MagnetURI should remain empty (not requested)")
	}
}

func TestSearchDownloads_IncludeAllExpandsToEveryFieldExceptTrackersAndFiles(t *testing.T) {
	client, _ := newQbitMockRoutes(t, map[string]mockRoute{
		"/api/v2/torrents/info": {body: fixture1Download},
	})
	out, terr := callSearchDownloads(t, client, SearchDownloadsInput{
		Hashes:        []string{"aaa"},
		IncludeFields: []string{"all"},
	})
	if terr != nil {
		t.Fatalf("unexpected error: %+v", terr)
	}
	if len(out.Downloads) != 1 {
		t.Fatalf("expected 1 download, got %d", len(out.Downloads))
	}
	d := out.Downloads[0]
	// Empty resolver under this test: every path falls through to
	// the unspecified-sentinel form.
	if d.SavePath != "unspecified:data/anime" {
		t.Errorf("SavePath = %q, want 'unspecified:data/anime'", d.SavePath)
	}
	if d.ContentPath != "unspecified:data/anime/show.mkv" {
		t.Errorf("ContentPath = %q, want 'unspecified:data/anime/show.mkv'", d.ContentPath)
	}
	if d.MagnetURI == "" {
		t.Error("MagnetURI should be populated by 'all'")
	}
	if d.AutoTMM == nil || !*d.AutoTMM {
		t.Error("AutoTMM should be true pointer")
	}
	if d.FirstLastPiecePrio == nil || !*d.FirstLastPiecePrio {
		t.Error("FirstLastPiecePrio should be true pointer")
	}
	if d.Private == nil || !*d.Private {
		t.Error("Private should be true pointer")
	}
	if d.Trackers != nil {
		t.Errorf("'all' should NOT include trackers; got %v", d.Trackers)
	}
	if d.Files != nil {
		t.Errorf("'all' should NOT include files; got %v", d.Files)
	}
}

func TestSearchDownloads_TrackersOrFilesRequireSingleHash(t *testing.T) {
	cases := []struct {
		name string
		in   SearchDownloadsInput
	}{
		{"no hashes", SearchDownloadsInput{IncludeFields: []string{"trackers"}}},
		{"multiple hashes", SearchDownloadsInput{Hashes: []string{"aaa", "bbb"}, IncludeFields: []string{"trackers"}}},
		{"with states filter", SearchDownloadsInput{Hashes: []string{"aaa"}, States: []NormalizedState{StateDownloading}, IncludeFields: []string{"trackers"}}},
		{"with tags filter", SearchDownloadsInput{Hashes: []string{"aaa"}, Tags: []string{"tvdb:*"}, IncludeFields: []string{"trackers"}}},
		{"files variant", SearchDownloadsInput{Hashes: []string{"aaa", "bbb"}, IncludeFields: []string{"files"}}},
	}
	client, _ := newQbitMock(t, fixture6Downloads)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, terr := callSearchDownloads(t, client, tc.in)
			if terr == nil || terr.Code != CodeInvalidArgument {
				t.Errorf("err = %+v, want invalid_argument", terr)
			}
		})
	}
}

func TestSearchDownloads_IncludeTrackersSingleHashSucceeds(t *testing.T) {
	client, captured := newQbitMockRoutes(t, map[string]mockRoute{
		"/api/v2/torrents/info":     {body: fixture1Download},
		"/api/v2/torrents/trackers": {body: fixtureTrackers},
	})
	out, terr := callSearchDownloads(t, client, SearchDownloadsInput{
		Hashes:        []string{"aaa"},
		IncludeFields: []string{"trackers"},
	})
	if terr != nil {
		t.Fatalf("unexpected error: %+v", terr)
	}
	if len(out.Downloads) != 1 || len(out.Downloads[0].Trackers) != 2 {
		t.Fatalf("expected 1 download with 2 trackers; got %+v", out.Downloads)
	}
	if out.Downloads[0].Trackers[0].Status != "working" {
		t.Errorf("tracker[0].Status = %q", out.Downloads[0].Trackers[0].Status)
	}
	if out.Downloads[0].Trackers[1].Status != "not_working" {
		t.Errorf("tracker[1].Status = %q", out.Downloads[0].Trackers[1].Status)
	}
	if captured["/api/v2/torrents/trackers"].Query.Get("hash") != "aaa" {
		t.Errorf("trackers fetch hash param = %q", captured["/api/v2/torrents/trackers"].Query.Get("hash"))
	}
}

func TestSearchDownloads_IncludeFilesSingleHashSucceeds(t *testing.T) {
	client, _ := newQbitMockRoutes(t, map[string]mockRoute{
		"/api/v2/torrents/info":  {body: fixture1Download},
		"/api/v2/torrents/files": {body: fixtureFiles},
	})
	out, terr := callSearchDownloads(t, client, SearchDownloadsInput{
		Hashes:        []string{"aaa"},
		IncludeFields: []string{"files"},
	})
	if terr != nil {
		t.Fatalf("unexpected error: %+v", terr)
	}
	if len(out.Downloads[0].Files) != 2 {
		t.Fatalf("expected 2 files; got %d", len(out.Downloads[0].Files))
	}
	if out.Downloads[0].Files[0].Name != "show/episode-01.mkv" {
		t.Errorf("file[0].Name = %q", out.Downloads[0].Files[0].Name)
	}
}

func TestSearchDownloads_IncludeTrackersAndFiles(t *testing.T) {
	client, _ := newQbitMockRoutes(t, map[string]mockRoute{
		"/api/v2/torrents/info":     {body: fixture1Download},
		"/api/v2/torrents/trackers": {body: fixtureTrackers},
		"/api/v2/torrents/files":    {body: fixtureFiles},
	})
	out, terr := callSearchDownloads(t, client, SearchDownloadsInput{
		Hashes:        []string{"aaa"},
		IncludeFields: []string{"trackers", "files"},
	})
	if terr != nil {
		t.Fatalf("unexpected error: %+v", terr)
	}
	if len(out.Downloads[0].Trackers) != 2 || len(out.Downloads[0].Files) != 2 {
		t.Errorf("expected 2 trackers + 2 files, got %d/%d", len(out.Downloads[0].Trackers), len(out.Downloads[0].Files))
	}
}

func TestSearchDownloads_TrackersFetchErrorFailsCall(t *testing.T) {
	client, _ := newQbitMockRoutes(t, map[string]mockRoute{
		"/api/v2/torrents/info":     {body: fixture1Download},
		"/api/v2/torrents/trackers": {status: http.StatusInternalServerError},
	})
	_, terr := callSearchDownloads(t, client, SearchDownloadsInput{
		Hashes:        []string{"aaa"},
		IncludeFields: []string{"trackers"},
	})
	if terr == nil || terr.Code != CodeUpstreamUnavailable {
		t.Errorf("err = %+v, want upstream_unavailable", terr)
	}
}

func TestSearchDownloads_StateFilter(t *testing.T) {
	client, _ := newQbitMock(t, fixture6Downloads)
	out, terr := callSearchDownloads(t, client, SearchDownloadsInput{
		States: []NormalizedState{StateDownloading},
	})
	if terr != nil {
		t.Fatalf("unexpected error: %+v", terr)
	}
	if out.Count != 3 {
		t.Errorf("count = %d, want 3", out.Count)
	}
	for _, d := range out.Downloads {
		if d.State != StateDownloading {
			t.Errorf("download %s has state %q", d.Hash, d.State)
		}
	}
}

func TestSearchDownloads_StateFilterCaseInsensitive(t *testing.T) {
	// Agent commonly emits "Downloading" / "DOWNLOADING" depending on
	// the surrounding prose. validateStates lowercases each entry; the
	// downstream filter matches the canonical lowercase form.
	client, _ := newQbitMock(t, fixture6Downloads)
	out, terr := callSearchDownloads(t, client, SearchDownloadsInput{
		States: []NormalizedState{"Downloading", "SEEDING"},
	})
	if terr != nil {
		t.Fatalf("unexpected error: %+v", terr)
	}
	if out.Count != 4 {
		t.Errorf("count = %d, want 4 (downloading + seeding match)", out.Count)
	}
}

func TestSearchDownloads_MultiStateFilter(t *testing.T) {
	client, _ := newQbitMock(t, fixture6Downloads)
	out, _ := callSearchDownloads(t, client, SearchDownloadsInput{
		States: []NormalizedState{StateDownloading, StateSeeding},
	})
	if out.Count != 4 {
		t.Errorf("count = %d, want 4", out.Count)
	}
}

func TestSearchDownloads_TagGlob(t *testing.T) {
	client, _ := newQbitMock(t, fixture6Downloads)
	out, _ := callSearchDownloads(t, client, SearchDownloadsInput{
		Tags: []string{"tvdb:*"},
	})
	if out.Count != 3 {
		t.Errorf("count = %d, want 3", out.Count)
	}
}

func TestSearchDownloads_TagLiteralExact(t *testing.T) {
	client, _ := newQbitMock(t, fixture6Downloads)
	out, _ := callSearchDownloads(t, client, SearchDownloadsInput{Tags: []string{"weekly"}})
	if out.Count != 2 {
		t.Errorf("count = %d, want 2", out.Count)
	}
}

func TestSearchDownloads_TagUnion(t *testing.T) {
	client, _ := newQbitMock(t, fixture6Downloads)
	out, _ := callSearchDownloads(t, client, SearchDownloadsInput{Tags: []string{"tvdb:*", "weekly"}})
	if out.Count != 4 {
		t.Errorf("count = %d, want 4", out.Count)
	}
}

func TestSearchDownloads_HashesPassedToUpstream(t *testing.T) {
	client, cap := newQbitMock(t, fixture6Downloads)
	_, _ = callSearchDownloads(t, client, SearchDownloadsInput{Hashes: []string{"aaa", "bbb"}})
	got := cap.Query.Get("hashes")
	if got != "aaa|bbb" {
		t.Errorf("upstream hashes param = %q, want aaa|bbb", got)
	}
}

func TestSearchDownloads_SortPassedToUpstream(t *testing.T) {
	client, cap := newQbitMock(t, fixture6Downloads)
	_, _ = callSearchDownloads(t, client, SearchDownloadsInput{Sort: "name_desc"})
	if cap.Query.Get("sort") != "name" || cap.Query.Get("reverse") != "true" {
		t.Errorf("sort/reverse = %q/%q", cap.Query.Get("sort"), cap.Query.Get("reverse"))
	}
}

func TestSearchDownloads_DefaultSortIsAddedOnDesc(t *testing.T) {
	client, cap := newQbitMock(t, fixture6Downloads)
	_, _ = callSearchDownloads(t, client, SearchDownloadsInput{})
	if cap.Query.Get("sort") != "added_on" || cap.Query.Get("reverse") != "true" {
		t.Errorf("default sort/reverse = %q/%q", cap.Query.Get("sort"), cap.Query.Get("reverse"))
	}
}

func TestSearchDownloads_Pagination(t *testing.T) {
	client, _ := newQbitMock(t, fixture6Downloads)
	page1, _ := callSearchDownloads(t, client, SearchDownloadsInput{Limit: 2, Offset: 0})
	if page1.Count != 2 || !page1.HasMore {
		t.Errorf("page1: count=%d has_more=%v", page1.Count, page1.HasMore)
	}
	page3, _ := callSearchDownloads(t, client, SearchDownloadsInput{Limit: 2, Offset: 4})
	if page3.Count != 2 || page3.HasMore {
		t.Errorf("page3: count=%d has_more=%v", page3.Count, page3.HasMore)
	}
	beyond, _ := callSearchDownloads(t, client, SearchDownloadsInput{Limit: 2, Offset: 100})
	if beyond.Count != 0 || beyond.HasMore {
		t.Errorf("beyond: count=%d has_more=%v", beyond.Count, beyond.HasMore)
	}
}

func TestSearchDownloads_LimitClampedToMax(t *testing.T) {
	client, _ := newQbitMock(t, fixture6Downloads)
	out, _ := callSearchDownloads(t, client, SearchDownloadsInput{Limit: 5000})
	if out.Count != 6 {
		t.Errorf("count = %d, want 6 (clamped to 200, fixture has 6)", out.Count)
	}
}

func TestSearchDownloads_RejectsNegativeLimit(t *testing.T) {
	client, _ := newQbitMock(t, fixture6Downloads)
	_, terr := callSearchDownloads(t, client, SearchDownloadsInput{Limit: -1})
	if terr == nil || terr.Code != CodeInvalidArgument {
		t.Errorf("err = %+v", terr)
	}
}

func TestSearchDownloads_RejectsNegativeOffset(t *testing.T) {
	client, _ := newQbitMock(t, fixture6Downloads)
	_, terr := callSearchDownloads(t, client, SearchDownloadsInput{Offset: -1})
	if terr == nil || terr.Code != CodeInvalidArgument {
		t.Errorf("err = %+v", terr)
	}
}

func TestSearchDownloads_RejectsUnknownSort(t *testing.T) {
	client, _ := newQbitMock(t, fixture6Downloads)
	_, terr := callSearchDownloads(t, client, SearchDownloadsInput{Sort: "ratio_asc"})
	if terr == nil || terr.Code != CodeInvalidArgument {
		t.Fatalf("err = %+v", terr)
	}
	if !strings.Contains(terr.Message, "added_on_desc") {
		t.Errorf("message should list valid options: %q", terr.Message)
	}
}

func TestSearchDownloads_RejectsMalformedTagPattern(t *testing.T) {
	client, _ := newQbitMock(t, fixture6Downloads)
	_, terr := callSearchDownloads(t, client, SearchDownloadsInput{Tags: []string{"tvdb:[unclosed"}})
	if terr == nil || terr.Code != CodeInvalidArgument {
		t.Fatalf("err = %+v", terr)
	}
}

func TestSearchDownloads_RejectsUnknownState(t *testing.T) {
	client, _ := newQbitMock(t, fixture6Downloads)
	_, terr := callSearchDownloads(t, client, SearchDownloadsInput{States: []NormalizedState{"bogus"}})
	if terr == nil || terr.Code != CodeInvalidArgument {
		t.Errorf("err = %+v", terr)
	}
}

func TestSearchDownloads_RejectsUnknownIncludeField(t *testing.T) {
	client, _ := newQbitMock(t, fixture6Downloads)
	_, terr := callSearchDownloads(t, client, SearchDownloadsInput{IncludeFields: []string{"nope"}})
	if terr == nil || terr.Code != CodeInvalidArgument {
		t.Fatalf("err = %+v", terr)
	}
	if !strings.Contains(terr.Message, "save_path") {
		t.Errorf("message should list valid options: %q", terr.Message)
	}
}

func TestSearchDownloads_Upstream500IsUnavailableRetriable(t *testing.T) {
	client := newQbitMockStatus(t, http.StatusInternalServerError)
	_, terr := callSearchDownloads(t, client, SearchDownloadsInput{})
	if terr == nil || terr.Code != CodeUpstreamUnavailable {
		t.Errorf("err = %+v", terr)
	}
	if terr != nil && !terr.Retriable {
		t.Error("upstream_unavailable should be retriable")
	}
}

func TestSearchDownloads_Upstream403IsForbidden(t *testing.T) {
	client := newQbitMockStatus(t, http.StatusForbidden)
	_, terr := callSearchDownloads(t, client, SearchDownloadsInput{})
	if terr == nil || terr.Code != CodeUpstreamForbidden {
		t.Errorf("err = %+v", terr)
	}
}

func TestSearchDownloads_EmptyResult(t *testing.T) {
	client, _ := newQbitMock(t, `[]`)
	out, terr := callSearchDownloads(t, client, SearchDownloadsInput{})
	if terr != nil {
		t.Fatalf("err = %+v", terr)
	}
	if out.Count != 0 || out.HasMore {
		t.Errorf("count=%d has_more=%v", out.Count, out.HasMore)
	}
	if out.Downloads == nil {
		t.Error("downloads should be non-nil empty")
	}
}

func TestSearchDownloads_ETASentinelNormalizedToMinusOne(t *testing.T) {
	client, _ := newQbitMock(t, fixture6Downloads)
	out, _ := callSearchDownloads(t, client, SearchDownloadsInput{})
	var movie *Download
	for i := range out.Downloads {
		if out.Downloads[i].Hash == "ccc" {
			movie = &out.Downloads[i]
		}
	}
	if movie == nil || movie.EtaSeconds != -1 {
		t.Errorf("EtaSeconds = %v, want -1", movie)
	}
}

func TestSearchDownloads_StateNormalizationMetaDLBecomesDownloading(t *testing.T) {
	client, _ := newQbitMock(t, fixture6Downloads)
	out, _ := callSearchDownloads(t, client, SearchDownloadsInput{})
	var eee *Download
	for i := range out.Downloads {
		if out.Downloads[i].Hash == "eee" {
			eee = &out.Downloads[i]
		}
	}
	if eee == nil || eee.State != StateDownloading {
		t.Errorf("metaDL state = %v", eee)
	}
}

func TestSearchDownloads_TagsSplitAndTrimmed(t *testing.T) {
	client, _ := newQbitMock(t, fixture6Downloads)
	out, _ := callSearchDownloads(t, client, SearchDownloadsInput{})
	var aaa *Download
	for i := range out.Downloads {
		if out.Downloads[i].Hash == "aaa" {
			aaa = &out.Downloads[i]
		}
	}
	if aaa == nil {
		t.Fatal("aaa missing")
	}
	want := []string{"tvdb:12345", "weekly"}
	if len(aaa.Tags) != len(want) || aaa.Tags[0] != want[0] || aaa.Tags[1] != want[1] {
		t.Errorf("Tags = %v, want %v", aaa.Tags, want)
	}
}

func TestSearchDownloads_EmptyTagsIsEmptySlice(t *testing.T) {
	client, _ := newQbitMock(t, fixture6Downloads)
	out, _ := callSearchDownloads(t, client, SearchDownloadsInput{})
	var ddd *Download
	for i := range out.Downloads {
		if out.Downloads[i].Hash == "ddd" {
			ddd = &out.Downloads[i]
		}
	}
	if ddd == nil || ddd.Tags == nil || len(ddd.Tags) != 0 {
		t.Errorf("Tags = %v, want empty non-nil slice", ddd)
	}
}

// --- add_download ---

const (
	validMagnet     = "magnet:?xt=urn:btih:AABBCCDDEEFFAABBCCDDEEFFAABBCCDDEEFFAABB&dn=show"
	validMagnetHash = "aabbccddeeffaabbccddeeffaabbccddeeffaabb"
)

// addRouteOK wires the upstream routes the idempotent add path touches:
//   - /api/v2/torrents/info — idempotent pre-check; empty body so the
//     handler treats every test hash as "not yet present" and proceeds to
//     the upstream add call.
//   - /api/v2/torrents/add  — the actual add, always "Ok." in tests.
func addRouteOK() map[string]mockRoute {
	return map[string]mockRoute{
		"/api/v2/torrents/info": {body: "[]"},
		"/api/v2/torrents/add":  {body: "Ok."},
	}
}

func TestAddDownload_SuccessReturnsHashLowercase(t *testing.T) {
	client, captured := newQbitMockRoutes(t, addRouteOK())
	out, terr := callAddDownload(t, client, mustResolver(t, ""), AddDownloadInput{Magnet: validMagnet})
	if terr != nil {
		t.Fatalf("err = %+v", terr)
	}
	if out.Hash != validMagnetHash {
		t.Errorf("hash = %q", out.Hash)
	}
	if !out.Accepted {
		t.Error("accepted should be true")
	}
	if captured["/api/v2/torrents/add"].PostForm.Get("urls") != validMagnet {
		t.Errorf("urls form = %q", captured["/api/v2/torrents/add"].PostForm.Get("urls"))
	}
}

func TestAddDownload_Base32HashDecodedToHex(t *testing.T) {
	base32Magnet := "magnet:?xt=urn:btih:VKVKVKVKVKVKVKVKVKVKVKVKVKVKVKVK"
	const wantHex = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	client, _ := newQbitMockRoutes(t, addRouteOK())
	out, terr := callAddDownload(t, client, mustResolver(t, ""), AddDownloadInput{Magnet: base32Magnet})
	if terr != nil {
		t.Fatalf("err = %+v", terr)
	}
	if out.Hash != wantHex {
		t.Errorf("hash = %q, want %q", out.Hash, wantHex)
	}
}

func TestAddDownload_TagsCommaJoined(t *testing.T) {
	client, captured := newQbitMockRoutes(t, addRouteOK())
	_, terr := callAddDownload(t, client, mustResolver(t, ""), AddDownloadInput{
		Magnet: validMagnet,
		Tags:   []string{"tvdb:12345", "weekly"},
	})
	if terr != nil {
		t.Fatalf("err = %+v", terr)
	}
	if captured["/api/v2/torrents/add"].PostForm.Get("tags") != "tvdb:12345,weekly" {
		t.Errorf("tags form = %q", captured["/api/v2/torrents/add"].PostForm.Get("tags"))
	}
}

func TestAddDownload_DestinationResolvedToSavepath(t *testing.T) {
	client, captured := newQbitMockRoutes(t, addRouteOK())
	resolver := mustResolver(t, "kura-inbox=/mnt/kura,downloads=/mnt/downloads")
	_, terr := callAddDownload(t, client, resolver, AddDownloadInput{
		Magnet:      validMagnet,
		Destination: "kura-inbox",
	})
	if terr != nil {
		t.Fatalf("err = %+v", terr)
	}
	if captured["/api/v2/torrents/add"].PostForm.Get("savepath") != "/mnt/kura" {
		t.Errorf("savepath form = %q", captured["/api/v2/torrents/add"].PostForm.Get("savepath"))
	}
}

func TestAddDownload_UnknownDestinationRejected(t *testing.T) {
	client, captured := newQbitMockRoutes(t, addRouteOK())
	_, terr := callAddDownload(t, client, mustResolver(t, "kura-inbox=/mnt/kura"), AddDownloadInput{
		Magnet:      validMagnet,
		Destination: "bogus",
	})
	if terr == nil || terr.Code != CodeInvalidArgument {
		t.Fatalf("err = %+v", terr)
	}
	if captured["/api/v2/torrents/add"].Method != "" {
		t.Error("upstream should not have been called")
	}
}

func TestAddDownload_EmptyDestinationOmitsSavepathKey(t *testing.T) {
	client, captured := newQbitMockRoutes(t, addRouteOK())
	_, _ = callAddDownload(t, client, mustResolver(t, ""), AddDownloadInput{Magnet: validMagnet})
	if _, present := captured["/api/v2/torrents/add"].PostForm["savepath"]; present {
		t.Error("savepath form key should be absent when destination is empty")
	}
}

func TestAddDownload_AutoTMMAlwaysFalse(t *testing.T) {
	client, captured := newQbitMockRoutes(t, addRouteOK())
	_, _ = callAddDownload(t, client, mustResolver(t, "kura-inbox=/mnt/kura"), AddDownloadInput{
		Magnet:      validMagnet,
		Destination: "kura-inbox",
	})
	if captured["/api/v2/torrents/add"].PostForm.Get("autoTMM") != "false" {
		t.Errorf("autoTMM form = %q, want false", captured["/api/v2/torrents/add"].PostForm.Get("autoTMM"))
	}
}

func TestAddDownload_RenamePassedThrough(t *testing.T) {
	client, captured := newQbitMockRoutes(t, addRouteOK())
	_, _ = callAddDownload(t, client, mustResolver(t, ""), AddDownloadInput{
		Magnet: validMagnet,
		Rename: "Custom Name S01E02",
	})
	if captured["/api/v2/torrents/add"].PostForm.Get("rename") != "Custom Name S01E02" {
		t.Errorf("rename form = %q", captured["/api/v2/torrents/add"].PostForm.Get("rename"))
	}
}

func TestAddDownload_EmptyMagnetRejected(t *testing.T) {
	client, captured := newQbitMockRoutes(t, addRouteOK())
	_, terr := callAddDownload(t, client, mustResolver(t, ""), AddDownloadInput{Magnet: ""})
	if terr == nil || terr.Code != CodeInvalidArgument {
		t.Errorf("err = %+v", terr)
	}
	if captured["/api/v2/torrents/add"].Method != "" {
		t.Error("upstream should not have been called")
	}
}

func TestAddDownload_NonMagnetURIRejected(t *testing.T) {
	client, _ := newQbitMockRoutes(t, addRouteOK())
	_, terr := callAddDownload(t, client, mustResolver(t, ""), AddDownloadInput{Magnet: "http://example.com/file.torrent"})
	if terr == nil || terr.Code != CodeInvalidArgument {
		t.Errorf("err = %+v", terr)
	}
}

func TestAddDownload_MagnetMissingBtihXTRejected(t *testing.T) {
	client, _ := newQbitMockRoutes(t, addRouteOK())
	_, terr := callAddDownload(t, client, mustResolver(t, ""), AddDownloadInput{Magnet: "magnet:?dn=just-a-display-name"})
	if terr == nil || terr.Code != CodeInvalidArgument {
		t.Errorf("err = %+v", terr)
	}
}

func TestAddDownload_MalformedHashLengthRejected(t *testing.T) {
	client, _ := newQbitMockRoutes(t, addRouteOK())
	_, terr := callAddDownload(t, client, mustResolver(t, ""), AddDownloadInput{Magnet: "magnet:?xt=urn:btih:abc123"})
	if terr == nil || terr.Code != CodeInvalidArgument {
		t.Errorf("err = %+v", terr)
	}
}

func TestAddDownload_TagWithCommaRejected(t *testing.T) {
	client, captured := newQbitMockRoutes(t, addRouteOK())
	_, terr := callAddDownload(t, client, mustResolver(t, ""), AddDownloadInput{
		Magnet: validMagnet,
		Tags:   []string{"good", "bad,tag"},
	})
	if terr == nil || terr.Code != CodeInvalidArgument {
		t.Errorf("err = %+v", terr)
	}
	if captured["/api/v2/torrents/add"].Method != "" {
		t.Error("upstream should not have been called")
	}
}

func TestAddDownload_Upstream500(t *testing.T) {
	client := newQbitMockStatus(t, http.StatusInternalServerError)
	_, terr := callAddDownload(t, client, mustResolver(t, ""), AddDownloadInput{Magnet: validMagnet})
	if terr == nil || terr.Code != CodeUpstreamUnavailable {
		t.Errorf("err = %+v", terr)
	}
}

func TestAddDownload_Upstream403(t *testing.T) {
	client := newQbitMockStatus(t, http.StatusForbidden)
	_, terr := callAddDownload(t, client, mustResolver(t, ""), AddDownloadInput{Magnet: validMagnet})
	if terr == nil || terr.Code != CodeUpstreamForbidden {
		t.Errorf("err = %+v", terr)
	}
}

func TestAddDownload_SuccessFlagsAlreadyExistedFalse(t *testing.T) {
	client, _ := newQbitMockRoutes(t, addRouteOK())
	out, terr := callAddDownload(t, client, mustResolver(t, ""), AddDownloadInput{Magnet: validMagnet})
	if terr != nil {
		t.Fatalf("err = %+v", terr)
	}
	if out.AlreadyExisted {
		t.Error("already_existed should be false for a fresh add")
	}
}

func TestAddDownload_IdempotentSkipsUpstreamWhenHashPresent(t *testing.T) {
	// /info returns a single torrent matching the magnet hash, so the
	// handler should skip the /add call entirely and report
	// already_existed=true.
	existing := `[{"hash":"` + validMagnetHash + `","name":"prior","state":"downloading"}]`
	client, captured := newQbitMockRoutes(t, map[string]mockRoute{
		"/api/v2/torrents/info": {body: existing},
		"/api/v2/torrents/add":  {body: "Ok."},
	})
	out, terr := callAddDownload(t, client, mustResolver(t, ""), AddDownloadInput{Magnet: validMagnet})
	if terr != nil {
		t.Fatalf("err = %+v", terr)
	}
	if !out.AlreadyExisted {
		t.Error("already_existed should be true when hash is already present")
	}
	if !out.Accepted {
		t.Error("accepted should still be true on the idempotent path")
	}
	if out.Hash != validMagnetHash {
		t.Errorf("hash = %q, want %q", out.Hash, validMagnetHash)
	}
	if captured["/api/v2/torrents/add"].Method != "" {
		t.Error("upstream /torrents/add should not be called when the hash is already present")
	}
}

func TestAddDownload_IdempotentAuditLogsAlreadyExisted(t *testing.T) {
	existing := `[{"hash":"` + validMagnetHash + `","name":"prior","state":"downloading"}]`
	client, _ := newQbitMockRoutes(t, map[string]mockRoute{
		"/api/v2/torrents/info": {body: existing},
		"/api/v2/torrents/add":  {body: "Ok."},
	})
	buf, logger := bufferedLogger()
	h := addDownloadHandler(client, mustResolver(t, ""), logger)
	if _, terr := h(context.Background(), AddDownloadInput{Magnet: validMagnet}); terr != nil {
		t.Fatalf("err = %+v", terr)
	}
	logOut := buf.String()
	if !strings.Contains(logOut, "audit=true") || !strings.Contains(logOut, "action=add") {
		t.Errorf("audit record not emitted: %s", logOut)
	}
	if !strings.Contains(logOut, "already_existed=true") {
		t.Errorf("audit record should include already_existed=true; got: %s", logOut)
	}
}

func TestAddDownload_PreCheckUpstreamErrorPropagates(t *testing.T) {
	// /info returns 500; the pre-check fails, the add call must not
	// occur, and the handler must surface upstream_unavailable.
	client, captured := newQbitMockRoutes(t, map[string]mockRoute{
		"/api/v2/torrents/info": {status: http.StatusInternalServerError},
		"/api/v2/torrents/add":  {body: "Ok."},
	})
	_, terr := callAddDownload(t, client, mustResolver(t, ""), AddDownloadInput{Magnet: validMagnet})
	if terr == nil || terr.Code != CodeUpstreamUnavailable {
		t.Errorf("err = %+v, want upstream_unavailable", terr)
	}
	if captured["/api/v2/torrents/add"].Method != "" {
		t.Error("upstream /torrents/add must not be called if pre-check fails")
	}
}

// --- remove_downloads ---

func removeRoutes() map[string]mockRoute {
	return map[string]mockRoute{
		"/api/v2/torrents/info":   {body: fixture6Downloads},
		"/api/v2/torrents/delete": {body: ""},
	}
}

func TestRemoveDownloads_HashesPath(t *testing.T) {
	client, captured := newQbitMockRoutes(t, removeRoutes())
	out, terr := callRemoveDownloads(t, client, RemoveDownloadsInput{HashesOrFilter: HashesOrFilter{Hashes: []string{"aaa", "bbb"}}})
	if terr != nil {
		t.Fatalf("err = %+v", terr)
	}
	if out.AffectedCount != 2 {
		t.Errorf("count = %d", out.AffectedCount)
	}
	if captured["/api/v2/torrents/delete"].PostForm.Get("hashes") != "aaa|bbb" {
		t.Errorf("upstream hashes form = %q", captured["/api/v2/torrents/delete"].PostForm.Get("hashes"))
	}
}

func TestRemoveDownloads_FilterPath(t *testing.T) {
	client, captured := newQbitMockRoutes(t, removeRoutes())
	out, terr := callRemoveDownloads(t, client, RemoveDownloadsInput{HashesOrFilter: HashesOrFilter{
		Filter: &FilterCriteria{States: []NormalizedState{StateDownloading}},
	}})
	if terr != nil {
		t.Fatalf("err = %+v", terr)
	}
	if out.AffectedCount != 3 {
		t.Errorf("count = %d, want 3", out.AffectedCount)
	}
	got := captured["/api/v2/torrents/delete"].PostForm.Get("hashes")
	for _, h := range []string{"aaa", "eee", "fff"} {
		if !strings.Contains(got, h) {
			t.Errorf("upstream hashes form %q missing %q", got, h)
		}
	}
}

func TestRemoveDownloads_FilterWithTagGlob(t *testing.T) {
	client, _ := newQbitMockRoutes(t, removeRoutes())
	out, terr := callRemoveDownloads(t, client, RemoveDownloadsInput{HashesOrFilter: HashesOrFilter{
		Filter: &FilterCriteria{Tags: []string{"tvdb:*"}},
	}})
	if terr != nil {
		t.Fatalf("err = %+v", terr)
	}
	if out.AffectedCount != 3 {
		t.Errorf("count = %d, want 3", out.AffectedCount)
	}
}

func TestRemoveDownloads_BothSelectorsRejected(t *testing.T) {
	client, _ := newQbitMockRoutes(t, removeRoutes())
	_, terr := callRemoveDownloads(t, client, RemoveDownloadsInput{HashesOrFilter: HashesOrFilter{
		Hashes: []string{"a"}, Filter: &FilterCriteria{},
	}})
	if terr == nil || terr.Code != CodeInvalidArgument {
		t.Errorf("err = %+v", terr)
	}
}

func TestRemoveDownloads_NeitherSelectorRejected(t *testing.T) {
	client, _ := newQbitMockRoutes(t, removeRoutes())
	_, terr := callRemoveDownloads(t, client, RemoveDownloadsInput{})
	if terr == nil || terr.Code != CodeInvalidArgument {
		t.Errorf("err = %+v", terr)
	}
}

func TestRemoveDownloads_EmptyHashesRejected(t *testing.T) {
	client, _ := newQbitMockRoutes(t, removeRoutes())
	_, terr := callRemoveDownloads(t, client, RemoveDownloadsInput{HashesOrFilter: HashesOrFilter{Hashes: []string{}}})
	if terr == nil || terr.Code != CodeInvalidArgument {
		t.Errorf("err = %+v", terr)
	}
}

func TestRemoveDownloads_FilterZeroMatchesSkipsUpstream(t *testing.T) {
	client, captured := newQbitMockRoutes(t, removeRoutes())
	out, terr := callRemoveDownloads(t, client, RemoveDownloadsInput{HashesOrFilter: HashesOrFilter{
		Filter: &FilterCriteria{Tags: []string{"nonexistent-tag-*"}},
	}})
	if terr != nil {
		t.Fatalf("err = %+v", terr)
	}
	if out.AffectedCount != 0 {
		t.Errorf("count = %d, want 0", out.AffectedCount)
	}
	if captured["/api/v2/torrents/delete"].Method != "" {
		t.Error("delete upstream should NOT have been called when filter matches zero")
	}
}

func TestRemoveDownloads_Upstream500IsUnavailable(t *testing.T) {
	client, _ := newQbitMockRoutes(t, map[string]mockRoute{
		"/api/v2/torrents/info":   {body: fixture6Downloads},
		"/api/v2/torrents/delete": {status: http.StatusInternalServerError},
	})
	_, terr := callRemoveDownloads(t, client, RemoveDownloadsInput{HashesOrFilter: HashesOrFilter{Hashes: []string{"aaa"}}})
	if terr == nil || terr.Code != CodeUpstreamUnavailable {
		t.Errorf("err = %+v", terr)
	}
}

func TestRemoveDownloads_Upstream403IsForbidden(t *testing.T) {
	client, _ := newQbitMockRoutes(t, map[string]mockRoute{
		"/api/v2/torrents/info":   {body: fixture6Downloads},
		"/api/v2/torrents/delete": {status: http.StatusForbidden},
	})
	_, terr := callRemoveDownloads(t, client, RemoveDownloadsInput{HashesOrFilter: HashesOrFilter{Hashes: []string{"aaa"}}})
	if terr == nil || terr.Code != CodeUpstreamForbidden {
		t.Errorf("err = %+v", terr)
	}
}

func TestRemoveDownloads_NoOnDiskDeletion(t *testing.T) {
	client, captured := newQbitMockRoutes(t, removeRoutes())
	_, terr := callRemoveDownloads(t, client, RemoveDownloadsInput{HashesOrFilter: HashesOrFilter{Hashes: []string{"aaa"}}})
	if terr != nil {
		t.Fatalf("err = %+v", terr)
	}
	if captured["/api/v2/torrents/delete"].PostForm.Get("deleteFiles") != "false" {
		t.Errorf("deleteFiles form = %q, want false (on-disk delete should never be exposed)", captured["/api/v2/torrents/delete"].PostForm.Get("deleteFiles"))
	}
}

// --- audit logging ---

func TestAuditLog_RemoveEmitsWarnRecord(t *testing.T) {
	buf, logger := bufferedLogger()
	client, _ := newQbitMockRoutes(t, removeRoutes())
	h := removeDownloadsHandler(client, logger)
	if _, terr := h(context.Background(), RemoveDownloadsInput{HashesOrFilter: HashesOrFilter{Hashes: []string{"aaa"}}}); terr != nil {
		t.Fatalf("err = %+v", terr)
	}
	got := buf.String()
	for _, want := range []string{"audit=true", "action=remove", "aaa", "level=WARN"} {
		if !strings.Contains(got, want) {
			t.Errorf("audit log missing %q in %q", want, got)
		}
	}
}

func TestAuditLog_AddEmitsRecordWithDestinationAndTags(t *testing.T) {
	buf, logger := bufferedLogger()
	client, _ := newQbitMockRoutes(t, addRouteOK())
	resolver := mustResolver(t, "kura-inbox=/mnt/kura")
	h := addDownloadHandler(client, resolver, logger)
	_, terr := h(context.Background(), AddDownloadInput{
		Magnet:      validMagnet,
		Destination: "kura-inbox",
		Tags:        []string{"tvdb:12345"},
	})
	if terr != nil {
		t.Fatalf("err = %+v", terr)
	}
	got := buf.String()
	for _, want := range []string{"audit=true", "action=add", validMagnetHash, "destination=kura-inbox", "tvdb:12345"} {
		if !strings.Contains(got, want) {
			t.Errorf("audit log missing %q in %q", want, got)
		}
	}
}

func TestAuditLog_NotEmittedOnValidationFailure(t *testing.T) {
	buf, logger := bufferedLogger()
	client, _ := newQbitMockRoutes(t, removeRoutes())
	h := removeDownloadsHandler(client, logger)
	if _, terr := h(context.Background(), RemoveDownloadsInput{}); terr == nil {
		t.Fatal("expected invalid_argument")
	}
	if strings.Contains(buf.String(), "audit=true") {
		t.Errorf("audit should not fire when validation rejects pre-flight: %q", buf.String())
	}
}
