Feature: Check mode reports without writing
  CI runs dbt-ditto to find out whether the documentation in the repository is
  the documentation the DAG implies. That run must report the difference and
  leave the working tree exactly as it found it.

  Background:
    Given a dbt project
    And a seed "raw" with columns:
      | column | description            |
      | id     | Identifier of the row. |
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

  Scenario: a run that would change something changes nothing
    When dbt-ditto runs in check mode
    Then changes are reported
    And no file on disk changed

  Scenario: the same run without check mode writes
    When dbt-ditto runs
    Then changes are reported
    And the file "models/_stg.yml" contains:
      """
      description: Identifier of the row.
      """

  Scenario: a second run has nothing left to do
    When dbt-ditto runs
    And dbt-ditto runs in check mode
    Then no changes are reported
