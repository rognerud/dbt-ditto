#!/usr/bin/env python3
"""Offline tests for the source providers.

Neither warehouse can be reached from a test run, and neither needs to be: what
is worth checking is the part that is easy to get wrong and cheap to check —
the contract, profiles.yml resolution, and the type rendering that has to agree
with what each dbt adapter writes into catalog.json.

Run with `python packaging/providers/test_providers.py`. No pytest, no
dependencies, because a test that needs its own install is a test that does not
run in the hook that matters.
"""

from __future__ import annotations

import io
import json
import os
import sys
import tempfile
import unittest
from dataclasses import dataclass

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

import dbt_ditto_provider as provider  # noqa: E402
import snowflake as sf  # noqa: E402


class TestContract(unittest.TestCase):
    def test_request_is_grouped_by_project(self):
        raw = {
            "version": 1,
            "projects": [
                {
                    "name": "shop",
                    "root": "/repo/shop",
                    "profile": "shop",
                    "target": "prod",
                    "profiles_dir": "/home/u/.dbt",
                }
            ],
            "sources": [
                {
                    "unique_id": "source.shop.crm.customers",
                    "database": "bq",
                    "schema": "raw",
                    "identifier": "customers",
                    "project": "shop",
                }
            ],
        }
        req = provider.read_request(io.StringIO(json.dumps(raw)))
        self.assertEqual(list(req.by_project()), ["shop"])
        self.assertEqual(req.projects["shop"].target, "prod")
        self.assertEqual(req.sources[0].fqn, "bq.raw.customers")

    def test_a_newer_contract_is_refused_rather_than_guessed_at(self):
        with self.assertRaises(SystemExit):
            provider.read_request(io.StringIO(json.dumps({"version": 99})))

    def test_empty_fields_are_left_out_of_the_response(self):
        doc = provider.Doc(unique_id="source.a.b.c")
        doc.columns.append(provider.Column(name="id"))
        self.assertEqual(
            doc.to_json(), {"unique_id": "source.a.b.c", "columns": [{"name": "id"}]}
        )


class TestProfiles(unittest.TestCase):
    """profiles.yml resolution, which is how a provider reuses dbt's own
    credentials instead of asking for a second copy of them."""

    def profile_dir(self, body: str) -> str:
        d = tempfile.mkdtemp()
        with open(os.path.join(d, "profiles.yml"), "w", encoding="utf-8") as fh:
            fh.write(body)
        return d

    def test_the_named_target_is_read(self):
        d = self.profile_dir(
            "shop:\n"
            "  target: dev\n"
            "  outputs:\n"
            "    dev:\n      type: bigquery\n      project: dev-project\n"
            "    prod:\n      type: bigquery\n      project: prod-project\n"
        )
        p = provider.Project("shop", "/repo", "shop", "prod", d)
        self.assertEqual(provider._load_profile_from_yaml(p)["project"], "prod-project")

    def test_the_profile_default_target_is_used_when_none_is_given(self):
        d = self.profile_dir(
            "shop:\n"
            "  target: dev\n"
            "  outputs:\n"
            "    dev:\n      type: bigquery\n      project: dev-project\n"
        )
        p = provider.Project("shop", "/repo", "shop", "", d)
        self.assertEqual(provider._load_profile_from_yaml(p)["project"], "dev-project")

    def test_env_var_is_resolved(self):
        d = self.profile_dir(
            "shop:\n"
            "  target: prod\n"
            "  outputs:\n"
            "    prod:\n      type: snowflake\n      account: \"{{ env_var('SF_ACCOUNT') }}\"\n"
        )
        os.environ["SF_ACCOUNT"] = "abc123"
        try:
            p = provider.Project("shop", "/repo", "shop", "prod", d)
            self.assertEqual(provider._load_profile_from_yaml(p)["account"], "abc123")
        finally:
            del os.environ["SF_ACCOUNT"]

    def test_env_var_default_is_used_and_a_missing_one_is_named(self):
        d = self.profile_dir(
            "shop:\n"
            "  target: prod\n"
            "  outputs:\n"
            "    prod:\n"
            "      type: snowflake\n"
            "      role: \"{{ env_var('SF_ROLE', 'reader') }}\"\n"
            "      warehouse: \"{{ env_var('SF_WH') }}\"\n"
        )
        p = provider.Project("shop", "/repo", "shop", "prod", d)
        os.environ.pop("SF_WH", None)
        with self.assertRaises(SystemExit) as caught:
            provider._load_profile_from_yaml(p)
        self.assertIn("SF_WH", str(caught.exception))

    def test_an_unknown_profile_says_which_file_was_read(self):
        d = self.profile_dir("other:\n  target: prod\n  outputs:\n    prod:\n      type: bigquery\n")
        p = provider.Project("shop", "/repo", "shop", "prod", d)
        with self.assertRaises(SystemExit) as caught:
            provider._load_profile_from_yaml(p)
        self.assertIn("profiles.yml", str(caught.exception))


# --- BigQuery -------------------------------------------------------------
#
# Stubs, not mocks of the client: what is under test is the translation from a
# schema field to the text dbt-bigquery writes, and the real SchemaField has no
# behaviour worth reproducing.


@dataclass
class Field:
    name: str
    field_type: str
    mode: str = "NULLABLE"
    description: str = ""
    fields: tuple = ()
    precision: int | None = None
    scale: int | None = None
    policy_tags: object = None


class TestBigQueryRendering(unittest.TestCase):
    def setUp(self):
        import bigquery as bq

        self.bq = bq

    def test_a_record_is_rendered_the_way_the_adapter_writes_it(self):
        field = Field(
            "customer",
            "RECORD",
            fields=(Field("first_name", "STRING"), Field("last_name", "STRING")),
        )
        self.assertEqual(
            self.bq.render_type(field),
            "STRUCT<`first_name` STRING, `last_name` STRING>",
        )

    def test_a_repeated_record_is_wrapped_in_array(self):
        field = Field("items", "RECORD", mode="REPEATED", fields=(Field("sku", "STRING"),))
        self.assertEqual(self.bq.render_type(field), "ARRAY<STRUCT<`sku` STRING>>")

    def test_numeric_keeps_its_precision(self):
        self.assertEqual(
            self.bq.render_type(Field("v", "NUMERIC", precision=38, scale=9)),
            "NUMERIC(38, 9)",
        )

    def test_nesting_yields_the_parent_and_every_dotted_leaf(self):
        # BigQuery's catalog query joins COLUMN_FIELD_PATHS, so dbt reports
        # both. Emitting only the leaves would lose `customer` itself.
        fields = [
            Field("id", "INT64"),
            Field(
                "customer",
                "RECORD",
                fields=(
                    Field("first_name", "STRING", description="Given name."),
                    Field("address", "RECORD", fields=(Field("city", "STRING"),)),
                ),
            ),
        ]
        got = self.bq.flatten(fields)
        self.assertEqual(
            [c.name for c in got],
            ["id", "customer", "customer.first_name", "customer.address", "customer.address.city"],
        )
        self.assertEqual([c.index for c in got], [1, 2, 3, 4, 5])
        self.assertEqual(got[2].description, "Given name.")

    def test_policy_tags_travel_as_an_extra_key_under_dbts_own_spelling(self):
        class Tags:
            names = ["projects/p/locations/eu/taxonomies/1/policyTags/2"]

        got = self.bq.flatten([Field("email", "STRING", policy_tags=Tags())])
        self.assertEqual(got[0].extra["policy_tags"], Tags.names)


# --- Snowflake ------------------------------------------------------------


class TestSnowflakeRendering(unittest.TestCase):
    def test_text_is_reported_as_varchar_with_its_length(self):
        self.assertEqual(sf.render_type("TEXT", 16777216, None, None), "VARCHAR(16777216)")

    def test_number_keeps_precision_and_scale(self):
        self.assertEqual(sf.render_type("NUMBER", None, 38, 0), "NUMBER(38,0)")

    def test_a_type_with_nothing_to_add_is_left_alone(self):
        self.assertEqual(sf.render_type("TIMESTAMP_NTZ", None, None, None), "TIMESTAMP_NTZ")

    def test_a_database_name_cannot_end_the_identifier_it_is_quoted_into(self):
        # Identifiers cannot be bound as parameters, so this is the one place a
        # value reaches the SQL text.
        self.assertEqual(sf.quote_ident('we"ird'), '"we""ird"')


class TestSnowflakeSchemaDescription(unittest.TestCase):
    """describe_schema against a fake cursor: the query shapes are fixed, and
    what matters is that the three result sets are stitched together correctly
    and that missing tags do not lose the comments."""

    class Cursor:
        def __init__(self, rows, tags_fail=False):
            self.rows, self.tags_fail, self.pending = rows, tags_fail, []

        def execute(self, sql, params=None):
            if "information_schema.columns" in sql:
                self.pending = self.rows["columns"]
            elif "information_schema.tables" in sql:
                self.pending = self.rows["tables"]
            else:
                if self.tags_fail:
                    raise RuntimeError("Insufficient privileges on ACCOUNT_USAGE")
                self.pending = self.rows["tags"]

        def fetchall(self):
            return self.pending

    def rows(self):
        return {
            "columns": [
                ("ORDERS", "ORDER_ID", 1, "The order.", "NUMBER", None, 38, 0),
                ("ORDERS", "EMAIL", 2, None, "TEXT", 16777216, None, None),
            ],
            "tables": [("ORDERS", "Orders as the CRM records them.")],
            "tags": [
                ("ORDERS", None, "OWNER", "crm-team"),
                ("ORDERS", "EMAIL", "CLASSIFICATION", "restricted"),
            ],
        }

    def source(self):
        return provider.Source(
            unique_id="source.shop.crm.orders",
            database="ANALYTICS",
            schema="RAW",
            identifier="ORDERS",
            project="shop",
        )

    def test_comments_types_and_tags_are_stitched_together(self):
        docs, warnings = sf.describe_schema(
            self.Cursor(self.rows()), "ANALYTICS", "RAW", [self.source()]
        )
        self.assertEqual(warnings, [])
        doc = docs[0]
        self.assertEqual(doc.description, "Orders as the CRM records them.")
        self.assertEqual(doc.labels, {"OWNER": "crm-team"})
        self.assertEqual([c.name for c in doc.columns], ["ORDER_ID", "EMAIL"])
        self.assertEqual(doc.columns[0].description, "The order.")
        self.assertEqual(doc.columns[1].data_type, "VARCHAR(16777216)")
        self.assertEqual(doc.columns[1].labels, {"CLASSIFICATION": "restricted"})

    def test_unreadable_tags_warn_without_losing_the_comments(self):
        docs, warnings = sf.describe_schema(
            self.Cursor(self.rows(), tags_fail=True), "ANALYTICS", "RAW", [self.source()]
        )
        self.assertEqual(docs[0].description, "Orders as the CRM records them.")
        self.assertEqual(docs[0].labels, {})
        self.assertEqual(len(warnings), 1)
        self.assertIn("tags not read", warnings[0])

    def test_a_table_with_no_columns_is_reported_rather_than_written_empty(self):
        rows = self.rows()
        rows["columns"] = []
        docs, warnings = sf.describe_schema(
            self.Cursor(rows), "ANALYTICS", "RAW", [self.source()]
        )
        self.assertEqual(docs, [])
        self.assertIn("no columns", warnings[0])


if __name__ == "__main__":
    unittest.main(verbosity=2)
