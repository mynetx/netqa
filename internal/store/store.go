// Package store is the SQLite persistence layer for netqa. It owns the schema
// and provides typed access to providers, networks, samples, outages,
// throughput, DNS results and power events. All timestamps are stored as UTC
// Unix-nanoseconds so post-sleep wall-clock jumps cannot corrupt ordering.
package store

import (
	"database/sql"
	"fmt"
	"time"

	_ "modernc.org/sqlite"

	"github.com/mynetx/netqa/internal/model"
)

// Store wraps a SQLite database handle.
type Store struct {
	db *sql.DB
}

// Open opens (creating if needed) the database at path, enables WAL, and applies
// the schema. It is safe to call repeatedly on the same path.
func Open(path string) (*Store, error) {
	// Busy timeout avoids "database is locked" under the daemon's concurrent writers.
	dsn := fmt.Sprintf("file:%s?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)", path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// Close closes the underlying database.
func (s *Store) Close() error { return s.db.Close() }

const schema = `
CREATE TABLE IF NOT EXISTS providers (
	id              INTEGER PRIMARY KEY AUTOINCREMENT,
	name            TEXT NOT NULL,
	target_down     REAL NOT NULL DEFAULT 0,
	target_up       REAL NOT NULL DEFAULT 0,
	notes           TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS networks (
	id          INTEGER PRIMARY KEY AUTOINCREMENT,
	provider_id INTEGER REFERENCES providers(id) ON DELETE SET NULL,
	ssid        TEXT NOT NULL DEFAULT '',
	gateway_mac TEXT NOT NULL DEFAULT '',
	isp_asn     TEXT NOT NULL DEFAULT '',
	label       TEXT NOT NULL DEFAULT '',
	UNIQUE(ssid, gateway_mac)
);
CREATE TABLE IF NOT EXISTS samples (
	id          INTEGER PRIMARY KEY AUTOINCREMENT,
	network_id  INTEGER NOT NULL REFERENCES networks(id) ON DELETE CASCADE,
	ts          INTEGER NOT NULL,
	target      TEXT NOT NULL,
	success     INTEGER NOT NULL,
	rtt_ms      REAL NOT NULL DEFAULT 0,
	vpn         INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_samples_net_ts ON samples(network_id, ts);
CREATE TABLE IF NOT EXISTS outages (
	id          INTEGER PRIMARY KEY AUTOINCREMENT,
	network_id  INTEGER NOT NULL REFERENCES networks(id) ON DELETE CASCADE,
	start_ts    INTEGER NOT NULL,
	end_ts      INTEGER,
	class       TEXT NOT NULL,
	vpn         INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_outages_net_ts ON outages(network_id, start_ts);
CREATE TABLE IF NOT EXISTS throughput (
	id          INTEGER PRIMARY KEY AUTOINCREMENT,
	network_id  INTEGER NOT NULL REFERENCES networks(id) ON DELETE CASCADE,
	ts          INTEGER NOT NULL,
	down_mbit   REAL NOT NULL,
	up_mbit     REAL NOT NULL,
	vpn         INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_throughput_net_ts ON throughput(network_id, ts);
CREATE TABLE IF NOT EXISTS dns_results (
	id          INTEGER PRIMARY KEY AUTOINCREMENT,
	network_id  INTEGER NOT NULL REFERENCES networks(id) ON DELETE CASCADE,
	ts          INTEGER NOT NULL,
	server      TEXT NOT NULL,
	host        TEXT NOT NULL,
	success     INTEGER NOT NULL,
	latency_ms  REAL NOT NULL DEFAULT 0,
	vpn         INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_dns_net_ts ON dns_results(network_id, ts);
CREATE TABLE IF NOT EXISTS traceroutes (
	id          INTEGER PRIMARY KEY AUTOINCREMENT,
	outage_id   INTEGER REFERENCES outages(id) ON DELETE CASCADE,
	network_id  INTEGER NOT NULL REFERENCES networks(id) ON DELETE CASCADE,
	ts          INTEGER NOT NULL,
	target      TEXT NOT NULL,
	raw         TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS power_events (
	id          INTEGER PRIMARY KEY AUTOINCREMENT,
	ts          INTEGER NOT NULL,
	kind        TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_power_ts ON power_events(ts);
`

func (s *Store) migrate() error {
	_, err := s.db.Exec(schema)
	return err
}

func toUnix(t time.Time) int64 { return t.UTC().UnixNano() }
func fromUnix(n int64) time.Time {
	if n == 0 {
		return time.Time{}
	}
	return time.Unix(0, n).UTC()
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// --- Providers ---

// UpsertProvider inserts a new provider (when ID==0) or updates an existing one,
// returning its id. This is how targets are edited (e.g. 40 -> 20 Mbit).
func (s *Store) UpsertProvider(p model.Provider) (int64, error) {
	if p.ID == 0 {
		res, err := s.db.Exec(
			`INSERT INTO providers(name, target_down, target_up, notes) VALUES(?,?,?,?)`,
			p.Name, p.TargetDownMbit, p.TargetUpMbit, p.Notes)
		if err != nil {
			return 0, err
		}
		return res.LastInsertId()
	}
	_, err := s.db.Exec(
		`UPDATE providers SET name=?, target_down=?, target_up=?, notes=? WHERE id=?`,
		p.Name, p.TargetDownMbit, p.TargetUpMbit, p.Notes, p.ID)
	return p.ID, err
}

// Providers returns all providers ordered by id.
func (s *Store) Providers() ([]model.Provider, error) {
	rows, err := s.db.Query(`SELECT id, name, target_down, target_up, notes FROM providers ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Provider
	for rows.Next() {
		var p model.Provider
		if err := rows.Scan(&p.ID, &p.Name, &p.TargetDownMbit, &p.TargetUpMbit, &p.Notes); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// --- Networks ---

// UpsertNetwork inserts or updates a network keyed by its (SSID, gateway MAC)
// fingerprint and returns its id. Enrichment fields (ISPASN, label, provider)
// are updated when non-empty so a VPN-blank ASN never wipes a known one.
func (s *Store) UpsertNetwork(n model.Network) (int64, error) {
	existing, err := s.NetworkByFingerprint(n.SSID, n.GatewayMAC)
	if err != nil {
		return 0, err
	}
	if existing == nil {
		res, err := s.db.Exec(
			`INSERT INTO networks(provider_id, ssid, gateway_mac, isp_asn, label) VALUES(?,?,?,?,?)`,
			n.ProviderID, n.SSID, n.GatewayMAC, n.ISPASN, n.Label)
		if err != nil {
			return 0, err
		}
		return res.LastInsertId()
	}
	// Preserve existing enrichment when the incoming value is blank.
	asn := existing.ISPASN
	if n.ISPASN != "" {
		asn = n.ISPASN
	}
	label := existing.Label
	if n.Label != "" {
		label = n.Label
	}
	provider := existing.ProviderID
	if n.ProviderID != nil {
		provider = n.ProviderID
	}
	_, err = s.db.Exec(
		`UPDATE networks SET provider_id=?, isp_asn=?, label=? WHERE id=?`,
		provider, asn, label, existing.ID)
	return existing.ID, err
}

// NetworkByFingerprint returns the network matching (ssid, gatewayMAC) or nil.
func (s *Store) NetworkByFingerprint(ssid, gatewayMAC string) (*model.Network, error) {
	row := s.db.QueryRow(
		`SELECT id, provider_id, ssid, gateway_mac, isp_asn, label FROM networks WHERE ssid=? AND gateway_mac=?`,
		ssid, gatewayMAC)
	return scanNetwork(row)
}

// Networks returns all networks ordered by id.
func (s *Store) Networks() ([]model.Network, error) {
	rows, err := s.db.Query(`SELECT id, provider_id, ssid, gateway_mac, isp_asn, label FROM networks ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Network
	for rows.Next() {
		n, err := scanNetwork(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *n)
	}
	return out, rows.Err()
}

type scanner interface {
	Scan(dest ...any) error
}

func scanNetwork(row scanner) (*model.Network, error) {
	var n model.Network
	var pid sql.NullInt64
	err := row.Scan(&n.ID, &pid, &n.SSID, &n.GatewayMAC, &n.ISPASN, &n.Label)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if pid.Valid {
		n.ProviderID = &pid.Int64
	}
	return &n, nil
}

// --- Samples ---

// InsertSample stores one probe result.
func (s *Store) InsertSample(sm model.Sample) error {
	_, err := s.db.Exec(
		`INSERT INTO samples(network_id, ts, target, success, rtt_ms, vpn) VALUES(?,?,?,?,?,?)`,
		sm.NetworkID, toUnix(sm.TS), sm.Target, boolToInt(sm.Success), sm.RTTms, boolToInt(sm.VPN))
	return err
}

// SamplesBetween returns samples for a network in [from, to] ordered by time.
func (s *Store) SamplesBetween(networkID int64, from, to time.Time) ([]model.Sample, error) {
	rows, err := s.db.Query(
		`SELECT id, network_id, ts, target, success, rtt_ms, vpn FROM samples
		 WHERE network_id=? AND ts BETWEEN ? AND ? ORDER BY ts`,
		networkID, toUnix(from), toUnix(to))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Sample
	for rows.Next() {
		var sm model.Sample
		var ts int64
		var success, vpn int
		if err := rows.Scan(&sm.ID, &sm.NetworkID, &ts, &sm.Target, &success, &sm.RTTms, &vpn); err != nil {
			return nil, err
		}
		sm.TS = fromUnix(ts)
		sm.Success = success == 1
		sm.VPN = vpn == 1
		out = append(out, sm)
	}
	return out, rows.Err()
}

// --- Outages ---

// OpenOutage creates a new ongoing outage and returns its id.
func (s *Store) OpenOutage(o model.Outage) (int64, error) {
	res, err := s.db.Exec(
		`INSERT INTO outages(network_id, start_ts, end_ts, class, vpn) VALUES(?,?,NULL,?,?)`,
		o.NetworkID, toUnix(o.Start), string(o.Class), boolToInt(o.VPN))
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// CloseOutage sets the end time of an open outage.
func (s *Store) CloseOutage(id int64, end time.Time) error {
	_, err := s.db.Exec(`UPDATE outages SET end_ts=? WHERE id=?`, toUnix(end), id)
	return err
}

// OngoingOutage returns the currently open outage for a network, or nil.
func (s *Store) OngoingOutage(networkID int64) (*model.Outage, error) {
	row := s.db.QueryRow(
		`SELECT id, network_id, start_ts, end_ts, class, vpn FROM outages
		 WHERE network_id=? AND end_ts IS NULL ORDER BY start_ts DESC LIMIT 1`, networkID)
	return scanOutage(row)
}

// OutagesBetween returns outages overlapping [from, to] ordered by start.
func (s *Store) OutagesBetween(networkID int64, from, to time.Time) ([]model.Outage, error) {
	rows, err := s.db.Query(
		`SELECT id, network_id, start_ts, end_ts, class, vpn FROM outages
		 WHERE network_id=? AND start_ts BETWEEN ? AND ? ORDER BY start_ts`,
		networkID, toUnix(from), toUnix(to))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Outage
	for rows.Next() {
		o, err := scanOutage(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *o)
	}
	return out, rows.Err()
}

func scanOutage(row scanner) (*model.Outage, error) {
	var o model.Outage
	var start int64
	var end sql.NullInt64
	var class string
	var vpn int
	err := row.Scan(&o.ID, &o.NetworkID, &start, &end, &class, &vpn)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	o.Start = fromUnix(start)
	if end.Valid {
		o.End = fromUnix(end.Int64)
	}
	o.Class = model.OutageClass(class)
	o.VPN = vpn == 1
	return &o, nil
}

// --- Throughput / DNS / Power / Traceroute ---

// InsertThroughput stores one speed-test result.
func (s *Store) InsertThroughput(t model.Throughput) error {
	_, err := s.db.Exec(
		`INSERT INTO throughput(network_id, ts, down_mbit, up_mbit, vpn) VALUES(?,?,?,?,?)`,
		t.NetworkID, toUnix(t.TS), t.DownMbit, t.UpMbit, boolToInt(t.VPN))
	return err
}

// ThroughputBetween returns throughput rows for a network in [from, to].
func (s *Store) ThroughputBetween(networkID int64, from, to time.Time) ([]model.Throughput, error) {
	rows, err := s.db.Query(
		`SELECT id, network_id, ts, down_mbit, up_mbit, vpn FROM throughput
		 WHERE network_id=? AND ts BETWEEN ? AND ? ORDER BY ts`,
		networkID, toUnix(from), toUnix(to))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Throughput
	for rows.Next() {
		var t model.Throughput
		var ts int64
		var vpn int
		if err := rows.Scan(&t.ID, &t.NetworkID, &ts, &t.DownMbit, &t.UpMbit, &vpn); err != nil {
			return nil, err
		}
		t.TS = fromUnix(ts)
		t.VPN = vpn == 1
		out = append(out, t)
	}
	return out, rows.Err()
}

// InsertDNS stores one DNS resolution probe.
func (s *Store) InsertDNS(d model.DNSResult) error {
	_, err := s.db.Exec(
		`INSERT INTO dns_results(network_id, ts, server, host, success, latency_ms, vpn) VALUES(?,?,?,?,?,?,?)`,
		d.NetworkID, toUnix(d.TS), d.Server, d.Host, boolToInt(d.Success), d.LatencyMs, boolToInt(d.VPN))
	return err
}

// InsertPowerEvent stores a sleep/wake transition.
func (s *Store) InsertPowerEvent(p model.PowerEvent) error {
	_, err := s.db.Exec(`INSERT INTO power_events(ts, kind) VALUES(?,?)`, toUnix(p.TS), string(p.Kind))
	return err
}

// PowerEventsBetween returns power events in [from, to] ordered by time.
func (s *Store) PowerEventsBetween(from, to time.Time) ([]model.PowerEvent, error) {
	rows, err := s.db.Query(
		`SELECT id, ts, kind FROM power_events WHERE ts BETWEEN ? AND ? ORDER BY ts`,
		toUnix(from), toUnix(to))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.PowerEvent
	for rows.Next() {
		var p model.PowerEvent
		var ts int64
		var kind string
		if err := rows.Scan(&p.ID, &ts, &kind); err != nil {
			return nil, err
		}
		p.TS = fromUnix(ts)
		p.Kind = model.PowerKind(kind)
		out = append(out, p)
	}
	return out, rows.Err()
}

// InsertTraceroute stores a captured traceroute against an outage.
func (s *Store) InsertTraceroute(t model.Traceroute) error {
	var outageID any
	if t.OutageID != 0 {
		outageID = t.OutageID
	}
	_, err := s.db.Exec(
		`INSERT INTO traceroutes(outage_id, network_id, ts, target, raw) VALUES(?,?,?,?,?)`,
		outageID, t.NetworkID, toUnix(t.TS), t.Target, t.Raw)
	return err
}
