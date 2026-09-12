Feature: Documentation flows down the DAG
  A column documented once, upstream, should be documented everywhere it is
  used. These are the promises an analyst relies on when they write a
  description on a seed and never think about it again.

  Background:
    Given a dbt project
    And a seed "raw" with columns:
      | column       | description              |
      | id           | Identifier of the row.   |
      | amount_cents | Amount, in cents.        |

  Scenario: an undocumented column inherits from its parent
    Given a model "stg" reading from "raw" with columns:
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
      description: Identifier of the row.
      """

  Scenario: a description written by hand is never overwritten
    Given a model "stg" reading from "raw" with columns:
      | column | description                        |
      | id     | What this table means by an id.    |
    And the file "models/_stg.yml":
      """
      version: 2
      models:
        - name: stg
          columns:
            - name: id
              description: What this table means by an id.
      """
    When dbt-ditto runs
    Then the file "models/_stg.yml" contains:
      """
      description: What this table means by an id.
      """
    And the file "models/_stg.yml" does not contain "Identifier of the row."

  Scenario: a placeholder upstream is not inherited
    Given a seed "raw_stub" with columns:
      | column  | description    |
      | stub_id | Not documented |
    And a model "stg_stub" reading from "raw_stub" with columns:
      | column  | description |
      | stub_id |             |
    And the file "models/_stg_stub.yml":
      """
      version: 2
      models:
        - name: stg_stub
          columns:
            - name: stub_id
      """
    When dbt-ditto runs
    Then the file "models/_stg_stub.yml" does not contain "Not documented"

  Scenario: where a description came from is recorded
    Given a model "stg" reading from "raw" with columns:
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
      osmosis_progenitor: seed.demo.raw
      """

  Scenario: a column the parent does not have is left alone
    Given a model "stg" reading from "raw" with columns:
      | column   | description |
      | local_id |             |
    And the file "models/_stg.yml":
      """
      version: 2
      models:
        - name: stg
          columns:
            - name: local_id
      """
    When dbt-ditto runs
    Then the file "models/_stg.yml" does not contain "description:"
