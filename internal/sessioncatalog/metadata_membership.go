package sessioncatalog

const metadataTopicIndex = `CREATE INDEX IF NOT EXISTS idx_catalog_topics_registered_metadata
 ON catalog_topics(scope,workspace_root_key,topic_id) WHERE metadata_present=1`

const registeredMetadataTopics = `SELECT scope,workspace_root,workspace_root_key,topic_id
 FROM catalog_topics INDEXED BY idx_catalog_topics_registered_metadata WHERE metadata_present=1`

const orphanMetadataPredicate = `metadata_present=0 AND NOT EXISTS (
 SELECT 1 FROM catalog_sessions s WHERE s.scope=catalog_topics.scope
 AND s.workspace_root_key=catalog_topics.workspace_root_key AND s.topic_id=catalog_topics.topic_id)`
