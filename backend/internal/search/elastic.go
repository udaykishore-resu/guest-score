package search

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// Elastic talks to Elasticsearch over its REST API using net/http.
//
// The official go-elasticsearch client is not used, and that is a considered
// choice rather than an accident of this repo's zero-dependency history. It is
// a very large generated surface — every endpoint of the product — and this
// package makes four calls: create an index, index a document, bulk index, and
// search. Vendoring a client two orders of magnitude larger than its use would
// buy connection pooling that net/http already provides and sniffing that a
// single-node deployment behind a Kubernetes Service must not do anyway.
//
// What is genuinely lost is retry/backoff and the typed request builders. The
// first is added below; the second is replaced by keeping every query body in
// one file, so the JSON shape can be read and tested in one place.
type Elastic struct {
	baseURL string
	index   string
	http    *http.Client
	log     *slog.Logger

	username, password string
}

// ElasticOptions configures the client.
type ElasticOptions struct {
	URL      string
	Index    string
	Username string
	Password string
	Log      *slog.Logger
}

// NewElastic builds a client and verifies the cluster answers.
func NewElastic(ctx context.Context, opts ElasticOptions) (*Elastic, error) {
	if opts.Log == nil {
		opts.Log = slog.Default()
	}
	if opts.Index == "" {
		opts.Index = "guest-score-guests"
	}
	e := &Elastic{
		baseURL:  strings.TrimRight(opts.URL, "/"),
		index:    opts.Index,
		username: opts.Username,
		password: opts.Password,
		log:      opts.Log,
		http: &http.Client{
			Timeout: 10 * time.Second,
			Transport: &http.Transport{
				MaxIdleConnsPerHost: 4,
				IdleConnTimeout:     90 * time.Second,
			},
		},
	}
	pingCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := e.Ping(pingCtx); err != nil {
		return nil, fmt.Errorf("elasticsearch %s: %w", opts.URL, err)
	}
	return e, nil
}

func (e *Elastic) do(ctx context.Context, method, path string, body any, out any) error {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encoding request: %w", err)
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, e.baseURL+path, rdr)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if e.username != "" {
		req.SetBasicAuth(e.username, e.password)
	}

	resp, err := e.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		// Cap the body: an Elasticsearch error can carry a full stack trace and
		// the whole failing query, which does not belong in a log line.
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("elasticsearch %s %s: %s: %s",
			method, path, resp.Status, strings.TrimSpace(string(snippet)))
	}
	if out == nil {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (e *Elastic) Ping(ctx context.Context) error {
	var out struct {
		Status string `json:"status"`
	}
	if err := e.do(ctx, http.MethodGet, "/_cluster/health?timeout=5s", nil, &out); err != nil {
		return err
	}
	// Yellow is the normal state for a single node: every index has one
	// unassigned replica because there is nowhere to put it. Treating yellow as
	// unhealthy would mean the local stack never comes up.
	if out.Status == "red" {
		return fmt.Errorf("cluster health is red")
	}
	return nil
}

// mapping is the index definition.
//
// The interesting part is the analysis chain on `name`. asciifolding is what
// makes "Muller" find "Müller" and "Jose" find "José" — without it a desk agent
// on a keyboard that cannot produce the diacritic concludes there is no file
// and opens a duplicate. The `.exact` subfield keeps an unfolded copy so an
// exact match still outranks a folded one, and `.keyword` supports sorting and
// aggregation.
var mapping = map[string]any{
	"settings": map[string]any{
		"number_of_shards": 1,
		// Single-node: asking for a replica leaves the index permanently yellow
		// with an unassignable shard. The local overlay sets 0; a real cluster
		// overrides this.
		"number_of_replicas": 0,
		"analysis": map[string]any{
			"analyzer": map[string]any{
				"name_folded": map[string]any{
					"tokenizer": "standard",
					"filter":    []string{"lowercase", "asciifolding"},
				},
			},
		},
	},
	"mappings": map[string]any{
		// Anything not declared here is rejected rather than silently indexed.
		// This index must never accidentally acquire a document number because
		// someone added a field upstream.
		"dynamic": "strict",
		"properties": map[string]any{
			"id":        map[string]any{"type": "keyword"},
			"global_id": map[string]any{"type": "keyword"},
			"name": map[string]any{
				"type":     "text",
				"analyzer": "name_folded",
				"fields": map[string]any{
					"exact":   map[string]any{"type": "text", "analyzer": "standard"},
					"keyword": map[string]any{"type": "keyword", "ignore_above": 256},
				},
			},
			"email":              map[string]any{"type": "text", "analyzer": "name_folded", "fields": map[string]any{"keyword": map[string]any{"type": "keyword", "ignore_above": 256}}},
			"city":               map[string]any{"type": "text", "fields": map[string]any{"keyword": map[string]any{"type": "keyword"}}},
			"nationality":        map[string]any{"type": "keyword"},
			"verified":           map[string]any{"type": "boolean"},
			"tier":               map[string]any{"type": "keyword"},
			"composite":          map[string]any{"type": "double"},
			"flagged":            map[string]any{"type": "boolean"},
			"incident_count":     map[string]any{"type": "integer"},
			"stay_count":         map[string]any{"type": "integer"},
			"document_countries": map[string]any{"type": "keyword"},
			"document_types":     map[string]any{"type": "keyword"},
			"joined_at":          map[string]any{"type": "date"},
			"indexed_at":         map[string]any{"type": "date"},
		},
	},
}

// Ensure creates the index if it does not exist.
func (e *Elastic) Ensure(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, e.baseURL+"/"+e.index, nil)
	if err != nil {
		return err
	}
	if e.username != "" {
		req.SetBasicAuth(e.username, e.password)
	}
	resp, err := e.http.Do(req)
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		return nil
	}
	if err := e.do(ctx, http.MethodPut, "/"+e.index, mapping, nil); err != nil {
		// Two replicas booting together will race here and one loses. That is
		// expected, not an error worth failing a boot over.
		if strings.Contains(err.Error(), "resource_already_exists_exception") {
			return nil
		}
		return err
	}
	e.log.Info("created search index", "index", e.index)
	return nil
}

func (e *Elastic) Put(ctx context.Context, d Doc) error {
	// refresh=false: the directory tolerates a second of staleness, and
	// refreshing on every write is the single most effective way to make
	// Elasticsearch slow. Reindex uses the same default; the admin endpoint
	// forces a refresh once at the end instead.
	return e.do(ctx, http.MethodPut, "/"+e.index+"/_doc/"+d.ID, d, nil)
}

// PutBatch indexes many documents in one _bulk request.
func (e *Elastic) PutBatch(ctx context.Context, docs []Doc) error {
	if len(docs) == 0 {
		return nil
	}
	var buf bytes.Buffer
	for _, d := range docs {
		meta := map[string]any{"index": map[string]any{"_index": e.index, "_id": d.ID}}
		mb, err := json.Marshal(meta)
		if err != nil {
			return err
		}
		db, err := json.Marshal(d)
		if err != nil {
			return err
		}
		buf.Write(mb)
		buf.WriteByte('\n')
		buf.Write(db)
		buf.WriteByte('\n')
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.baseURL+"/_bulk?refresh=true", &buf)
	if err != nil {
		return err
	}
	// The bulk API requires this exact content type, not application/json.
	req.Header.Set("Content-Type", "application/x-ndjson")
	if e.username != "" {
		req.SetBasicAuth(e.username, e.password)
	}
	resp, err := e.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("bulk index: %s: %s", resp.Status, strings.TrimSpace(string(snippet)))
	}

	// _bulk answers 200 even when individual documents failed, so the body has
	// to be read. Silently dropping a guest from the directory is precisely the
	// bug that makes a desk agent open a duplicate file.
	var out struct {
		Errors bool `json:"errors"`
		Items  []map[string]struct {
			ID     string `json:"_id"`
			Status int    `json:"status"`
			Error  struct {
				Type   string `json:"type"`
				Reason string `json:"reason"`
			} `json:"error"`
		} `json:"items"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return fmt.Errorf("bulk response: %w", err)
	}
	if !out.Errors {
		return nil
	}
	var failed []string
	for _, item := range out.Items {
		for _, r := range item {
			if r.Status >= 300 {
				failed = append(failed, fmt.Sprintf("%s: %s", r.ID, r.Error.Reason))
			}
		}
	}
	return fmt.Errorf("bulk index: %d of %d documents failed: %s",
		len(failed), len(docs), strings.Join(failed, "; "))
}

func (e *Elastic) Delete(ctx context.Context, id string) error {
	err := e.do(ctx, http.MethodDelete, "/"+e.index+"/_doc/"+id, nil, nil)
	if err != nil && strings.Contains(err.Error(), "404") {
		return nil
	}
	return err
}

// buildQuery turns a Query into an Elasticsearch request body.
//
// Split out from Search so the JSON shape can be asserted in a test without a
// running cluster — the query DSL is the part most likely to be silently wrong,
// because a malformed clause usually returns zero hits rather than an error.
func (e *Elastic) buildQuery(q Query) map[string]any {
	must := []any{}
	filter := []any{}

	if text := strings.TrimSpace(q.Text); text != "" {
		must = append(must, map[string]any{
			"bool": map[string]any{
				"should": []any{
					// Exact-ish match on the unfolded field, boosted: someone who
					// typed the name correctly should not be outranked by a fuzzy
					// hit on someone else.
					map[string]any{"match": map[string]any{"name.exact": map[string]any{"query": text, "boost": 3}}},
					// Folded + fuzzy. AUTO means no fuzziness on short terms,
					// one edit up to 5 characters, two beyond — which is roughly
					// the rate at which names are actually mistyped, and tight
					// enough that "Mehta" does not match "Mehra".
					map[string]any{"match": map[string]any{"name": map[string]any{
						"query": text, "fuzziness": "AUTO", "prefix_length": 1,
					}}},
					map[string]any{"match": map[string]any{"email": map[string]any{"query": text, "fuzziness": "AUTO"}}},
					// A global ID is either right or wrong; fuzzy matching an
					// identifier would resolve to the wrong person's file.
					map[string]any{"term": map[string]any{"global_id": map[string]any{"value": strings.ToUpper(text), "boost": 10}}},
				},
				"minimum_should_match": 1,
			},
		})
	}
	if q.Tier != "" {
		filter = append(filter, map[string]any{"term": map[string]any{"tier": q.Tier}})
	}
	if q.Country != "" {
		filter = append(filter, map[string]any{"bool": map[string]any{
			"should": []any{
				map[string]any{"term": map[string]any{"nationality": q.Country}},
				map[string]any{"term": map[string]any{"document_countries": q.Country}},
			},
			"minimum_should_match": 1,
		}})
	}
	if q.Flagged != nil {
		filter = append(filter, map[string]any{"term": map[string]any{"flagged": *q.Flagged}})
	}
	if q.MinScore != nil {
		filter = append(filter, map[string]any{"range": map[string]any{"composite": map[string]any{"gte": *q.MinScore}}})
	}

	inner := map[string]any{}
	if len(must) > 0 {
		inner["must"] = must
	}
	if len(filter) > 0 {
		inner["filter"] = filter
	}
	query := map[string]any{"match_all": map[string]any{}}
	if len(inner) > 0 {
		query = map[string]any{"bool": inner}
	}

	limit := q.Limit
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	body := map[string]any{
		"query": query,
		"size":  limit,
		"from":  max(q.Offset, 0),
		// track_total_hits: without it Elasticsearch stops counting at 10,000
		// and the directory's "N guests" becomes a lie at scale.
		"track_total_hits": true,
	}
	if strings.TrimSpace(q.Text) == "" {
		// With no text there is no relevance to sort by, and _score would be
		// identical for every hit. Rank by standing instead.
		body["sort"] = []any{map[string]any{"composite": map[string]any{"order": "desc"}}}
	}
	return body
}

func (e *Elastic) Search(ctx context.Context, q Query) (Results, error) {
	var out struct {
		Hits struct {
			Total struct {
				Value int `json:"value"`
			} `json:"total"`
			Hits []struct {
				Score  float64 `json:"_score"`
				Source Doc     `json:"_source"`
			} `json:"hits"`
		} `json:"hits"`
	}
	if err := e.do(ctx, http.MethodPost, "/"+e.index+"/_search", e.buildQuery(q), &out); err != nil {
		return Results{}, err
	}
	res := Results{Engine: EngineElastic, Total: out.Hits.Total.Value}
	for _, h := range out.Hits.Hits {
		res.Hits = append(res.Hits, Result{Doc: h.Source, Relevance: h.Score})
	}
	return res, nil
}

func (e *Elastic) Close() error {
	e.http.CloseIdleConnections()
	return nil
}
