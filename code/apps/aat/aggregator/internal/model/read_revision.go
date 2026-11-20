package model

import "gorm.io/gorm"

// Every canonical change inserts an outbox row in the same transaction. Updating
// one revision row serializes commit visibility, unlike max(outbox.id), where an
// older sequence value can commit after a newer one. The trigger covers old
// Aggregator binaries too, so a rolling deployment cannot bypass invalidation.
func migrateReadRevision(tx *gorm.DB) error {
	return tx.Exec(`
CREATE TABLE IF NOT EXISTS hazard_read_revision (
 id integer PRIMARY KEY CHECK (id = 1), revision bigint NOT NULL
);
INSERT INTO hazard_read_revision (id, revision) VALUES (1, 1) ON CONFLICT DO NOTHING;
CREATE OR REPLACE FUNCTION invalidate_hazard_pages() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 UPDATE hazard_read_revision SET revision = revision + 1 WHERE id = 1;
 RETURN NEW;
END $$;
DO $$ BEGIN
 IF NOT EXISTS (SELECT 1 FROM pg_trigger
                WHERE tgrelid = 'hazard_outboxes'::regclass AND tgname = 'hazard_pages_changed') THEN
  CREATE TRIGGER hazard_pages_changed AFTER INSERT ON hazard_outboxes
  FOR EACH ROW EXECUTE FUNCTION invalidate_hazard_pages();
 END IF;
END $$;`).Error
}
