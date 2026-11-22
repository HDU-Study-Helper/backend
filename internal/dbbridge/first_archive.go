package dbbridge

import (
	"context"
	"fmt"
	"time"

	"academic/backend/internal/model"
	"github.com/jackc/pgx/v5"
)

// Add the host-owned time predicate to both standard data tools. Callers keep
// their original projections, filters, index pagination and connection ACLs.
func firstDataBounds(ctx context.Context, owner, dataset string, args *[]any) []string {
	since, asOf, ok := model.FirstReadWindow(ctx, owner)
	if !ok {
		return nil
	}
	var expr string
	switch dataset {
	case "academic_items":
		expr = "CASE WHEN " + usablePublication + " THEN a.published_at END"
	case "platform_records":
		expr = firstPlatformPublication()
	case "dws_messages", "dws_attachments": // attachments inherit the sending message's boundary
		expr = "a.sent_at"
	case "schedules":
		year, term := firstSemester(asOf)
		return []string{fmt.Sprintf("((a.source='TIMETABLE' AND a.academic_year=%d AND a.term=%d) OR (a.source<>'TIMETABLE' AND %s))", year, term, firstBetween("a.start_time", since, asOf, args))}
	default: // academic_offerings is the static course directory
		return nil
	}
	clause := firstBetween(expr, since, asOf, args)
	if dataset == "platform_records" {
		year, term := firstSemester(asOf)
		clause = fmt.Sprintf("(%s OR (r.source='jwxt' AND r.kind IN ('course','schedule') AND r.content#>>'{fields,academic_year}'='%d' AND r.content#>>'{fields,term}'='%d'))", clause, year, term)
	}
	return []string{clause}
}

func firstBetween(expr string, since, asOf time.Time, args *[]any) string {
	i := len(*args) + 1
	*args = append(*args, since, asOf)
	return fmt.Sprintf("(%s) BETWEEN $%d::timestamptz AND $%d::timestamptz", expr, i, i+1)
}

func firstSemester(at time.Time) (year, term int) {
	d := at.In(academicLocation)
	year, term = d.Year(), 1
	if d.Month() < 8 {
		year--
		if d.Month() >= 2 {
			term = 2
		}
	}
	return
}

// The supported database is PostgreSQL 17. Validate strings before parsing so
// malformed source dates cannot fail the request. Accept only complete source
// timestamps (including epoch), never a date without time or observation time.
func firstPlatformPublication() string {
	raw := `btrim(COALESCE(NULLIF(` + sourceFields("published_at_raw", "published_at", "publish_time", "publishTime", "publishedTime", "firstPublishTime") + `,''),
 NULLIF(CASE WHEN r.kind='notice' THEN ` + sourceFields("sendTime") + ` WHEN r.kind IN ('discussion','discussion_reply_user') THEN ` + sourceFields("postTime", "create_time", "createTime", "createtime", "replyTime", "reply_created_at_raw", "gmtCreate") + ` WHEN r.source='mooc' THEN ` + sourceFields("releaseTime") + ` END,''),''))`
	return `(SELECT CASE WHEN value ~ '^[0-9]{9,10}$' THEN to_timestamp(value::double precision)
 WHEN value ~ '^[0-9]{12,13}$' THEN to_timestamp(value::double precision/1000)
 WHEN value ~ '^[0-9]{4}-[0-9]{1,2}-[0-9]{1,2}[ T][0-9]{1,2}:[0-9]{2}(:[0-9]{2}(\.[0-9]{1,9})?)?(Z|[+-][0-9]{2}:[0-9]{2})$' AND pg_input_is_valid(value,'timestamp with time zone') THEN value::timestamptz
 WHEN value ~ '^[0-9]{4}[-/.年][0-9]{1,2}[-/.月][0-9]{1,2}日?[ T][0-9]{1,2}:[0-9]{2}(:[0-9]{2}(\.[0-9]{1,9})?)?$' AND pg_input_is_valid(translate(value,'/.年月日','----'),'timestamp without time zone') THEN translate(value,'/.年月日','----')::timestamp AT TIME ZONE 'Asia/Shanghai'
 END FROM (SELECT ` + raw + ` AS value) publication)`
}

func firstPublicationGaps(ctx context.Context, tx pgx.Tx, owner string, p *model.FirstPacket) error {
	p.Missing = map[string]int{}
	rows, err := tx.Query(ctx, `SELECT a.source||'/'||a.kind,count(*) FROM academic_items a JOIN platform_connections c ON (c.user_id,c.id)=(a.user_id,a.connection_id) WHERE a.user_id=$1 AND c.enabled AND c.state='active' AND (a.published_at IS NULL OR NOT coalesce(`+usablePublication+`,false)) GROUP BY a.source,a.kind
 UNION ALL SELECT 'dws/message',count(*) FROM dws_messages a JOIN dws_connections c ON (c.user_id,c.id)=(a.user_id,a.connection_id) JOIN dws_groups g ON (g.user_id,g.connection_id,g.id)=(a.user_id,a.connection_id,a.group_id) WHERE a.user_id=$1 AND c.enabled AND c.state='active' AND a.sent_at IS NULL HAVING count(*)>0
 UNION ALL SELECT r.source||'/'||r.kind,count(*) FROM platform_records r JOIN platform_connections c ON (c.user_id,c.id)=(r.user_id,r.connection_id) WHERE r.user_id=$1 AND c.enabled AND c.state='active' AND r.kind NOT IN ('course','schedule') AND NOT EXISTS(SELECT 1 FROM academic_item_evidence e WHERE (e.user_id,e.connection_id,e.record_id)=(r.user_id,r.connection_id,r.id)) AND `+firstPlatformPublication()+` IS NULL GROUP BY r.source,r.kind`, owner)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var kind string
		var n int
		if err = rows.Scan(&kind, &n); err != nil {
			return err
		}
		p.Missing[kind] += n
	}
	return rows.Err()
}
