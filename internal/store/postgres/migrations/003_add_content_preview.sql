DO $$
BEGIN
    -- Add the column if it doesn't exist
    IF NOT EXISTS (
        SELECT 1 FROM information_schema.columns 
        WHERE table_name='documents' AND column_name='content_preview'
    ) THEN
        ALTER TABLE documents ADD COLUMN content_preview TEXT;
    END IF;
END $$;

-- Update existing documents to have a preview
UPDATE documents SET content_preview = LEFT(content, 60) WHERE content_preview IS NULL;

-- Create an index for faster lookups
CREATE INDEX IF NOT EXISTS idx_documents_content_preview ON documents(LOWER(content_preview));
