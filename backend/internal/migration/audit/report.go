package audit

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

const (
	ReportFormatVersion = 1
	AuditVersion        = "p04-audit-v1"
	ExitCodeOK          = 0
	ExitCodeBlocked     = 2
)

type Classification string

const (
	ClassificationExpected  Classification = "expected"
	ClassificationExplained Classification = "explained"
	ClassificationBlocking  Classification = "blocking"
)

type IntegrityResult struct {
	Check string `json:"check"`
	OK    bool   `json:"ok"`
	Count int64  `json:"count"`
	Code  string `json:"code"`
}

type TableMetric struct {
	Table              string `json:"table"`
	RowCount           int64  `json:"row_count"`
	PrimaryKey         string `json:"primary_key"`
	PrimaryMinRecordID string `json:"primary_min_record_id,omitempty"`
	PrimaryMaxRecordID string `json:"primary_max_record_id,omitempty"`
	SchemaSHA256       string `json:"schema_sha256"`
}

type RelationMetric struct {
	Relation        string   `json:"relation"`
	Count           int64    `json:"count"`
	RecordIDs       []string `json:"record_ids,omitempty"`
	RecordSetSHA256 string   `json:"record_set_sha256"`
	Truncated       bool     `json:"truncated"`
	Evaluated       bool     `json:"evaluated"`
}

type PathMetric struct {
	Table           string   `json:"table"`
	Column          string   `json:"column"`
	Classification  string   `json:"classification"`
	Count           int64    `json:"count"`
	RecordIDs       []string `json:"record_ids,omitempty"`
	RecordSetSHA256 string   `json:"record_set_sha256"`
	Truncated       bool     `json:"truncated"`
	Blocking        bool     `json:"blocking"`
	Evaluated       bool     `json:"evaluated"`
}

type AggregateMetric struct {
	Invariant       string   `json:"invariant"`
	Count           int64    `json:"count"`
	RecordIDs       []string `json:"record_ids,omitempty"`
	RecordSetSHA256 string   `json:"record_set_sha256"`
	Truncated       bool     `json:"truncated"`
	Evaluated       bool     `json:"evaluated"`
}

type Finding struct {
	Classification Classification `json:"classification"`
	Category       string         `json:"category"`
	Code           string         `json:"code"`
	Table          string         `json:"table,omitempty"`
	Column         string         `json:"column,omitempty"`
	Count          int64          `json:"count"`
	RecordIDs      []string       `json:"record_ids,omitempty"`
}

type RowDifference struct {
	Category          string         `json:"category"`
	Name              string         `json:"name"`
	Table             string         `json:"table"`
	Column            string         `json:"column,omitempty"`
	Before            int64          `json:"before"`
	After             int64          `json:"after"`
	Delta             int64          `json:"delta"`
	BeforeFingerprint string         `json:"before_fingerprint,omitempty"`
	AfterFingerprint  string         `json:"after_fingerprint,omitempty"`
	Classification    Classification `json:"classification"`
	MigrationVersion  int            `json:"migration_version,omitempty"`
	ReasonCode        string         `json:"reason_code"`
}

type Comparison struct {
	OK          bool            `json:"ok"`
	Differences []RowDifference `json:"differences"`
}

type Report struct {
	FormatVersion    int               `json:"format_version"`
	AuditVersion     string            `json:"audit_version"`
	Phase            string            `json:"phase"`
	MigrationVersion int               `json:"migration_version"`
	OK               bool              `json:"ok"`
	Integrity        []IntegrityResult `json:"integrity"`
	Tables           []TableMetric     `json:"tables"`
	Relations        []RelationMetric  `json:"relations"`
	AuthorizedRoots  []string          `json:"authorized_roots"`
	Paths            []PathMetric      `json:"paths"`
	Aggregates       []AggregateMetric `json:"aggregates"`
	Findings         []Finding         `json:"findings"`
	Comparison       *Comparison       `json:"comparison,omitempty"`
}

type DifferenceRule struct {
	Category         string
	Name             string
	Table            string
	Column           string
	Delta            int64
	Classification   Classification
	MigrationVersion int
	ReasonCode       string
}

func Compare(before, after Report, rules []DifferenceRule) Comparison {
	beforeMetrics := comparisonMetrics(before)
	afterMetrics := comparisonMetrics(after)
	ruleByMetric := make(map[string]DifferenceRule, len(rules))
	duplicateRules := make(map[string]struct{})
	for _, rule := range rules {
		key := differenceRuleKey(rule)
		if key == "" || rule.MigrationVersion <= before.MigrationVersion ||
			rule.MigrationVersion > after.MigrationVersion || safeLabel(rule.ReasonCode) == "" ||
			safeLabel(rule.ReasonCode) == "invalid" ||
			(rule.Classification != ClassificationExpected && rule.Classification != ClassificationExplained) {
			continue
		}
		if _, duplicate := ruleByMetric[key]; duplicate {
			delete(ruleByMetric, key)
			duplicateRules[key] = struct{}{}
			continue
		}
		if _, duplicate := duplicateRules[key]; duplicate {
			continue
		}
		ruleByMetric[key] = rule
	}
	keys := make(map[string]struct{}, len(beforeMetrics)+len(afterMetrics))
	for key := range beforeMetrics {
		keys[key] = struct{}{}
	}
	for key := range afterMetrics {
		keys[key] = struct{}{}
	}
	ordered := make([]string, 0, len(keys))
	for key := range keys {
		ordered = append(ordered, key)
	}
	sort.Strings(ordered)
	comparison := Comparison{OK: true, Differences: make([]RowDifference, 0, len(ordered))}
	for _, key := range ordered {
		beforeMetric, beforePresent := beforeMetrics[key]
		afterMetric := afterMetrics[key]
		identity := beforeMetric
		if !beforePresent {
			identity = afterMetric
		}
		delta := afterMetric.value - beforeMetric.value
		_, afterPresent := afterMetrics[key]
		fingerprintChanged := beforePresent && afterPresent && beforeMetric.fingerprint != afterMetric.fingerprint
		changed := delta != 0 || fingerprintChanged
		if identity.category != "table" && !changed {
			continue
		}
		classification := ClassificationExpected
		reason := "metric_unchanged"
		if identity.category == "table" {
			reason = "row_count_unchanged"
		}
		migrationVersion := 0
		if changed {
			rule, ok := ruleByMetric[key]
			if !ok || rule.Delta != delta {
				classification, reason = ClassificationBlocking, "unexplained_row_delta"
				if identity.category != "table" {
					reason = "unexplained_audit_delta"
				} else if delta == 0 {
					reason = "unexplained_primary_key_range"
				}
				comparison.OK = false
			} else {
				classification, reason = rule.Classification, rule.ReasonCode
				migrationVersion = rule.MigrationVersion
			}
		}
		comparison.Differences = append(comparison.Differences, RowDifference{
			Category: identity.category, Name: identity.name, Table: identity.table, Column: identity.column,
			Before: beforeMetric.value, After: afterMetric.value, Delta: delta,
			BeforeFingerprint: beforeMetric.fingerprint, AfterFingerprint: afterMetric.fingerprint,
			Classification: classification, MigrationVersion: migrationVersion, ReasonCode: reason,
		})
	}
	return comparison
}

func (report *Report) Normalize() {
	sort.Slice(report.Integrity, func(i, j int) bool { return report.Integrity[i].Check < report.Integrity[j].Check })
	sort.Slice(report.Tables, func(i, j int) bool { return report.Tables[i].Table < report.Tables[j].Table })
	sort.Slice(report.Relations, func(i, j int) bool { return report.Relations[i].Relation < report.Relations[j].Relation })
	sort.Strings(report.AuthorizedRoots)
	sort.Slice(report.Paths, func(i, j int) bool {
		left, right := report.Paths[i], report.Paths[j]
		return left.Table+"\x00"+left.Column+"\x00"+left.Classification < right.Table+"\x00"+right.Column+"\x00"+right.Classification
	})
	sort.Slice(report.Aggregates, func(i, j int) bool { return report.Aggregates[i].Invariant < report.Aggregates[j].Invariant })
	sort.Slice(report.Findings, func(i, j int) bool {
		left, right := report.Findings[i], report.Findings[j]
		return string(left.Classification)+"\x00"+left.Category+"\x00"+left.Code+"\x00"+left.Table+"\x00"+left.Column <
			string(right.Classification)+"\x00"+right.Category+"\x00"+right.Code+"\x00"+right.Table+"\x00"+right.Column
	})
	for index := range report.Relations {
		sort.Strings(report.Relations[index].RecordIDs)
	}
	for index := range report.Paths {
		sort.Strings(report.Paths[index].RecordIDs)
	}
	for index := range report.Aggregates {
		sort.Strings(report.Aggregates[index].RecordIDs)
	}
	for index := range report.Findings {
		sort.Strings(report.Findings[index].RecordIDs)
	}
	if report.Comparison != nil {
		sort.Slice(report.Comparison.Differences, func(i, j int) bool {
			left, right := report.Comparison.Differences[i], report.Comparison.Differences[j]
			return left.Category+"\x00"+left.Name+"\x00"+left.Table+"\x00"+left.Column <
				right.Category+"\x00"+right.Name+"\x00"+right.Table+"\x00"+right.Column
		})
		report.Comparison.OK = true
		for _, difference := range report.Comparison.Differences {
			if difference.Classification == ClassificationBlocking {
				report.Comparison.OK = false
			}
		}
	}
	report.OK = true
	for _, result := range report.Integrity {
		if !result.OK {
			report.OK = false
		}
	}
	for _, finding := range report.Findings {
		if finding.Classification == ClassificationBlocking {
			report.OK = false
		}
	}
	if report.Comparison != nil && !report.Comparison.OK {
		report.OK = false
	}
}

func (report Report) JSON() ([]byte, error) {
	copy := report
	copy.Normalize()
	encoded, err := json.MarshalIndent(copy, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(encoded, '\n'), nil
}

func (report Report) HumanSummary() string {
	copy := report
	copy.Normalize()
	blocking := int64(0)
	for _, finding := range copy.Findings {
		if finding.Classification == ClassificationBlocking {
			blocking += finding.Count
		}
	}
	if copy.Comparison != nil {
		for _, difference := range copy.Comparison.Differences {
			if difference.Classification == ClassificationBlocking {
				blocking++
			}
		}
	}
	status := "ok"
	if !copy.OK {
		status = "blocked"
	}
	return fmt.Sprintf("audit=%s phase=%s schema=%d status=%s tables=%d blocking=%d exit=%d\n",
		copy.AuditVersion, safeLabel(copy.Phase), copy.MigrationVersion, status, len(copy.Tables), blocking, copy.ExitCode())
}

func (report Report) ExitCode() int {
	report.Normalize()
	if report.OK {
		return ExitCodeOK
	}
	return ExitCodeBlocked
}

type comparisonMetric struct {
	category    string
	name        string
	table       string
	column      string
	value       int64
	fingerprint string
}

func comparisonMetrics(report Report) map[string]comparisonMetric {
	result := make(map[string]comparisonMetric, len(report.Tables)+len(report.Relations)+len(report.Paths)+len(report.Aggregates))
	for _, table := range report.Tables {
		fingerprint := ""
		if table.PrimaryMinRecordID != "" || table.PrimaryMaxRecordID != "" {
			fingerprint = sha256Text(table.PrimaryMinRecordID + "\x00" + table.PrimaryMaxRecordID)
		}
		metric := comparisonMetric{
			category: "table", name: table.Table, table: table.Table, value: table.RowCount, fingerprint: fingerprint,
		}
		result[comparisonMetricKey(metric)] = metric
	}
	for _, relation := range report.Relations {
		metric := comparisonMetric{category: "relation", name: relation.Relation, value: relation.Count, fingerprint: relation.RecordSetSHA256}
		result[comparisonMetricKey(metric)] = metric
	}
	for _, path := range report.Paths {
		metric := comparisonMetric{category: "path", name: path.Classification, table: path.Table, column: path.Column, value: path.Count, fingerprint: path.RecordSetSHA256}
		result[comparisonMetricKey(metric)] = metric
	}
	for _, aggregate := range report.Aggregates {
		metric := comparisonMetric{category: "aggregate", name: aggregate.Invariant, value: aggregate.Count, fingerprint: aggregate.RecordSetSHA256}
		result[comparisonMetricKey(metric)] = metric
	}
	return result
}

func comparisonMetricKey(metric comparisonMetric) string {
	return metric.category + "\x00" + metric.name + "\x00" + metric.table + "\x00" + metric.column
}

func differenceRuleKey(rule DifferenceRule) string {
	category := rule.Category
	name := rule.Name
	if category == "" && rule.Table != "" {
		category, name = "table", rule.Table
	}
	if safeLabel(category) == "invalid" || safeLabel(category) == "" || name == "" {
		return ""
	}
	return comparisonMetricKey(comparisonMetric{category: category, name: name, table: rule.Table, column: rule.Column})
}

func safeLabel(value string) string {
	value = strings.TrimSpace(value)
	for _, char := range value {
		if !((char >= 'a' && char <= 'z') || (char >= '0' && char <= '9') || char == '_' || char == '-') {
			return "invalid"
		}
	}
	return value
}
