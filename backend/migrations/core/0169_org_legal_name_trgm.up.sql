-- PO-F-2 scores an organization's legal_name as well as its display_name:
-- two records can carry the same registered entity under different marketing
-- names, and that pair is a duplicate the queue must see. The fuzzy tier
-- narrows its candidate set with a trigram predicate, so legal_name needs the
-- same index display_name already has (0077_apostrophe_fold) or every create
-- pays a sequential scan of the table.
CREATE INDEX idx_org_legal_name_trgm
    ON organization USING gin (f_fold_apostrophes(lower(legal_name)) gin_trgm_ops);
