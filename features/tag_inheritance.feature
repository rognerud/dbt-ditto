Feature: A sensitivity label travels down the DAG without being decided twice
  A tag that names a thing — pii, nightly — can be unioned down the graph. A tag
  that carries a value cannot: a column fed by a public table and a confidential
  one has one sensitivity, not two. These are the promises somebody relies on
  when they classify a source table once and expect every mart built from it to
  agree, and when they overrule that classification on one column and expect the
  overrule to survive every later run.

  Background:
    Given a dbt project
    And the config:
      """
      tags:
        rules:
          - key: data-sensitivity
            values: [public, internal, confidential, sensitive]
      """

  Scenario: the strictest source decides, and the decision is marked as this tool's
    Given a seed "raw_public" with columns:
      | column | description          | tags                    |
      | id     | Identifier of a row. | data-sensitivity:public |
    And a seed "raw_private" with columns:
      | column | description | tags                          |
      | id     |             | data-sensitivity:confidential |
    And a model "stg" reading from "raw_public" and "raw_private" with columns:
      | column | description |
      | id     |             |
    And the file "models/_stg.yml":
      """
      version: 2
      models:
        - name: stg
          columns:
            - name: id
      """
    When dbt-ditto runs
    Then the file "models/_stg.yml" contains:
      """
      - data-sensitivity:confidential
      """
    And the file "models/_stg.yml" does not contain "data-sensitivity:public"
    And the file "models/_stg.yml" contains:
      """
      dbt_ditto_label_defaults:
        - data-sensitivity
      """

  Scenario: a value this tool wrote last run is revised when the source gets stricter
    Given a seed "raw" with columns:
      | column | description          | tags                       |
      | id     | Identifier of a row. | data-sensitivity:sensitive |
    And a model "stg" reading from "raw" with columns:
      | column | description | tags                      | meta                                               |
      | id     |             | data-sensitivity:internal | {dbt_ditto_label_defaults: [data-sensitivity]}     |
    And the file "models/_stg.yml":
      """
      version: 2
      models:
        - name: stg
          columns:
            - name: id
              tags:
                - data-sensitivity:internal
              meta:
                dbt_ditto_label_defaults:
                  - data-sensitivity
      """
    When dbt-ditto runs
    Then the file "models/_stg.yml" contains:
      """
      - data-sensitivity:sensitive
      """
    And the file "models/_stg.yml" does not contain "data-sensitivity:internal"

  Scenario: a value somebody wrote is not revised, however strict the source
    Given a seed "raw" with columns:
      | column | description          | tags                       |
      | id     | Identifier of a row. | data-sensitivity:sensitive |
    And a model "stg" reading from "raw" with columns:
      | column | description | tags                      |
      | id     |             | data-sensitivity:internal |
    And the file "models/_stg.yml":
      """
      version: 2
      models:
        - name: stg
          columns:
            - name: id
              tags:
                - data-sensitivity:internal
      """
    When dbt-ditto runs
    Then the file "models/_stg.yml" contains:
      """
      - data-sensitivity:internal
      """
    And the file "models/_stg.yml" does not contain "data-sensitivity:sensitive"
    And the file "models/_stg.yml" does not contain "dbt_ditto_label_defaults"

  Scenario: running twice writes the same thing, so a rule is not a source of churn
    Given a seed "raw" with columns:
      | column | description          | tags                          |
      | id     | Identifier of a row. | data-sensitivity:confidential |
    And a model "stg" reading from "raw" with columns:
      | column | description |
      | id     |             |
    And the file "models/_stg.yml":
      """
      version: 2
      models:
        - name: stg
          columns:
            - name: id
      """
    When dbt-ditto runs
    And dbt-ditto runs in check mode
    Then no changes are reported
    And the file "models/_stg.yml" contains:
      """
      - data-sensitivity:confidential
      """

  Scenario: declassifying a column once declassifies everything built from it
    Given a seed "raw" with columns:
      | column | description        | tags                       |
      | email  | The email address. | data-sensitivity:sensitive |
    And a model "stg_hashed" reading from "raw" with columns:
      | column | description | tags                      |
      | email  |             | data-sensitivity:internal |
    And a model "dim" reading from "stg_hashed" with columns:
      | column | description |
      | email  |             |
    And the file "models/_stg_hashed.yml":
      """
      version: 2
      models:
        - name: stg_hashed
          columns:
            - name: email
              tags:
                - data-sensitivity:internal
      """
    And the file "models/_dim.yml":
      """
      version: 2
      models:
        - name: dim
          columns:
            - name: email
      """
    When dbt-ditto runs
    Then the file "models/_dim.yml" contains:
      """
      - data-sensitivity:internal
      """
    And the file "models/_dim.yml" does not contain "data-sensitivity:sensitive"
    And the file "models/_stg_hashed.yml" does not contain "dbt_ditto_label_defaults"

  Scenario: an unaltered branch still carries its own source the whole way down
    Given a seed "raw" with columns:
      | column | description        | tags                       |
      | email  | The email address. | data-sensitivity:sensitive |
    And a seed "raw_other" with columns:
      | column | description | tags                          |
      | email  |             | data-sensitivity:confidential |
    And a model "stg_hashed" reading from "raw" with columns:
      | column | description | tags                    |
      | email  |             | data-sensitivity:public |
    And a model "dim" reading from "stg_hashed" and "raw_other" with columns:
      | column | description |
      | email  |             |
    And the file "models/_stg_hashed.yml":
      """
      version: 2
      models:
        - name: stg_hashed
          columns:
            - name: email
              tags:
                - data-sensitivity:public
      """
    And the file "models/_dim.yml":
      """
      version: 2
      models:
        - name: dim
          columns:
            - name: email
      """
    When dbt-ditto runs
    Then the file "models/_dim.yml" contains:
      """
      - data-sensitivity:confidential
      """

  Scenario: a value outside the vocabulary is reported rather than compared
    Given a seed "raw" with columns:
      | column | description          | tags                    |
      | id     | Identifier of a row. | data-sensitivity:secrit |
    And a model "stg" reading from "raw" with columns:
      | column | description |
      | id     |             |
    And the file "models/_stg.yml":
      """
      version: 2
      models:
        - name: stg
          columns:
            - name: id
      """
    When dbt-ditto runs
    Then the file "models/_stg.yml" contains:
      """
      - data-sensitivity:secrit
      """
