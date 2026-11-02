ALTER TABLE budget_jobs ADD COLUMN terminal_reduction_id text REFERENCES budget_model_reductions(id);
