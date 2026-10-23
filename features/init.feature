Feature: Getting configured
  A single dbt project needs no configuration at all, and standing in one and
  typing `dbt-ditto inherit` should say so rather than fail. Several projects
  need a file, and `dbt-ditto init` writes it from what is on disk instead of
  from a page of documentation.

  Scenario: the working directory's own project is used when there is no config
    Given a dbt project
    And a seed "raw_orders" documenting "order_id" as "Surrogate key for an order."
    And an undocumented model "stg_orders" reading from "raw_orders" with column "order_id"
    When I run `dbt-ditto inherit`
    Then the exit code is 0
    And it warns "no dbt_ditto.yml found; using the dbt project in ."
    And the file "models/_stg_orders.yml" contains:
      """
      description: Surrogate key for an order.
      """

  Scenario: the note names the command that writes a config
    Given a dbt project
    And a seed "raw" documenting "id" as "Identifier of the row."
    And an undocumented model "stg" reading from "raw" with column "id"
    When I run `dbt-ditto inherit`
    Then it warns "run `dbt-ditto init` to configure several projects"

  Scenario: init writes a config for the project it finds
    Given a dbt project
    When I run `dbt-ditto init --yes`
    Then the exit code is 0
    And it prints "1 dbt project"
    And it prints "wrote dbt_ditto.yml"
    And the file "dbt_ditto.yml" on disk contains:
      """
      projects:
        - path: "."
      """

  Scenario: the config init writes is the config inherit then reads
    Given a dbt project
    And a seed "raw_orders" documenting "order_id" as "Surrogate key for an order."
    And an undocumented model "stg_orders" reading from "raw_orders" with column "order_id"
    When I run `dbt-ditto init --yes`
    And I run `dbt-ditto inherit`
    Then the exit code is 0
    And it does not print "no dbt_ditto.yml found"
    And the file "models/_stg_orders.yml" contains:
      """
      description: Surrogate key for an order.
      """

  Scenario: a config already there is never overwritten by accident
    Given a dbt project
    When I run `dbt-ditto init --yes`
    And I run `dbt-ditto init --yes`
    Then the exit code is 1
    And it warns "dbt_ditto.yml already exists"
    And it warns "--force"

  Scenario: --stdout writes nothing
    Given a dbt project
    When I run `dbt-ditto init --yes --stdout`
    Then the exit code is 0
    And it prints "projects:"
    And there is no file "dbt_ditto.yml" on disk

  Scenario: there is nothing to configure without a dbt project
    Given a dbt project
    When I run `dbt-ditto init --yes models`
    Then the exit code is 1
    And it warns "no dbt_project.yml found"

  Scenario: every setting is explained by the help
    Given a dbt project
    When I run `dbt-ditto help config`
    Then the exit code is 0
    And it prints "inheritance:"
    And it prints "Every key is optional."
    And it prints:
      """
      columns                [true]
      """
