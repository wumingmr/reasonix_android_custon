package agent

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reasonix/internal/fileops"
	"reasonix/internal/projectiondb"
	"reasonix/internal/store"
)

type displayPagerEventSource struct {
	fingerprint           string
	eventInfo             os.FileInfo
	eventVersion          fileops.Version
	dag, schemaOne, plain bool
}

func observeDisplayPagerEvents(ctx context.Context, source, head string, forceSource, plain bool, indexInfo os.FileInfo, fingerprint, stored string, storedErr error) (*displayPagerEventSource, error) {
	var err error
	var eventInfo os.FileInfo
	var eventVersion fileops.Version
	dag := false
	schemaOne := false
	hasDAG := false
	if eventInfo, err = os.Stat(store.SessionEventLog(source)); err == nil && eventInfo.Size() > 0 {
		eventTarget, version := fileops.DiskSnapshot(store.SessionEventLog(source), eventInfo)
		eventVersion = version
		fingerprint += fmt.Sprintf(":event:%s:%s", eventTarget.Key, eventVersion)
		// Reuse the proven kind for unchanged schema-1 sources. A legacy JSON
		// writer may place its identifying fields after a huge messages array;
		// rediscovering that header on every cached open would reread the array.
		if plain && head == "" && storedErr == nil && stored == fingerprint+":schema1" {
			schemaOne = true
		} else {
			schema, kind, known, probeErr := validatedDisplayEventKind(ctx, source)
			if probeErr != nil {
				return nil, probeErr
			}
			hasDAG = known && schema == sessionDAGSchemaVersion
			dag = hasDAG && (plain || head != "" || forceSource)
			schemaOne = known && schema == sessionEventSchemaVersion && (kind == sessionEventTypeReplace || kind == sessionEventTypeAppend) && plain
		}
		if dag {
			fingerprint += fmt.Sprintf(":dag:%d:%d:%s", eventInfo.Size(), eventInfo.ModTime().UnixNano(), head)
		} else if schemaOne {
			fingerprint += ":schema1"
		}
	} else if err != nil && !os.IsNotExist(err) {
		return nil, err
	} else {
		// Missing and empty logs carry no authority over a checkpoint. Record
		// that absence explicitly so Validate can detect a newly written log.
		eventInfo = nil
	}
	if head != "" && !dag {
		return nil, errors.New("requested branch has no DAG source")
	}
	if dag || schemaOne {
		plain = false
	}
	if plain {
		// A checkpoint is only authoritative in the absence of an event log.
		// Event/DAG readers must establish the selected view before indexing.
		if err := validateCheckpointDisplaySource(source); err != nil {
			return nil, err
		}
		fingerprint += ":checkpoint"
	} else if !dag && !schemaOne {
		fingerprint += fmt.Sprintf(":%d:%d", indexInfo.Size(), indexInfo.ModTime().UnixNano())
	}
	return &displayPagerEventSource{fingerprint, eventInfo, eventVersion, dag, schemaOne, plain}, nil
}

func validatedDisplayEventKind(ctx context.Context, source string) (int, string, bool, error) {
	f, openErr := fileops.OpenReplaceableRead(store.SessionEventLog(source))
	if openErr != nil {
		return 0, "", false, openErr
	}
	schema, kind, known, probeErr := probeDisplayEventHeader(ctx, f)
	_ = f.Close()
	if probeErr != nil {
		return 0, "", false, probeErr
	}
	if known && (schema > sessionDAGSchemaVersion || schema == sessionEventSchemaVersion && kind != sessionEventTypeReplace && kind != sessionEventTypeAppend) {
		return 0, "", false, fmt.Errorf("%w: event schema %d type %q", ErrDisplayFormatUnsupported, schema, kind)
	}
	return schema, kind, known, nil
}

func (p *DisplayPager) loadMatchingHeader(ctx context.Context, info os.FileInfo, identity PersistedState, known bool, head string) (bool, error) {
	var header string
	if err := p.DB.QueryRowContext(ctx, `SELECT value FROM metadata WHERE key='header'`).Scan(&header); err != nil {
		return false, err
	}
	if err := json.Unmarshal([]byte(header), &p.Header); err != nil {
		return false, err
	}
	return !(p.Header.TranscriptSize != info.Size() || known && head == "" && (p.Header.ContentDigest != identity.DigestHex || p.Header.RevisionKnown != identity.RevisionKnown || p.Header.RevisionKnown && p.Header.Revision != identity.Revision)), nil
}

func validateCheckpointDisplaySource(source string) error {
	if logInfo, logErr := os.Stat(store.SessionEventLog(source)); logErr == nil && logInfo.Size() > 0 {
		return ErrDisplayFormatUnsupported
	} else if logErr != nil && !os.IsNotExist(logErr) {
		return logErr
	}
	return nil
}

func (s *displayPagerEventSource) build(ctx context.Context, db *sql.DB, source, indexPath, head string, checkpointSize int64) error {
	if s.dag {
		return buildDAGDisplayPager(ctx, db, source, s.fingerprint, head, checkpointSize)
	}
	if s.schemaOne {
		return buildEventDisplayPager(ctx, db, source, s.fingerprint, checkpointSize)
	}
	if s.plain {
		return buildCheckpointDisplayPager(ctx, db, source, s.fingerprint)
	}
	return importDisplayPager(ctx, db, indexPath, s.fingerprint)
}

func (s *displayPagerEventSource) rebuild(ctx context.Context, opts projectiondb.OpenOptions, source, indexPath, head string, size int64) error {
	if s.plain {
		opts.ResumeKey = "checkpoint-v1:" + s.fingerprint
	} else if s.schemaOne {
		opts.ResumeKey = "event-v1:" + s.fingerprint
	} else if s.dag {
		opts.ResumeKey = "dag-v1:" + s.fingerprint
	} else if !s.dag && !s.schemaOne {
		opts.ResumeKey = "display-import-v1:" + s.fingerprint
	}
	return projectiondb.Rebuild(ctx, opts, func(ctx context.Context, db *sql.DB) error {
		return s.build(ctx, db, source, indexPath, head, size)
	})
}
