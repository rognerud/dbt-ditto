Feature: Documentation carried back up to a source
  A source table is a root of the DAG, so nothing upstream can ever document
  it — but the documentation usually exists one step downstream, in the staging
  model that reads the source. Backfill puts it where it belongs, and only ever
  fills blanks.

  Background:
    Given a dbt project
    And a source "crm.orders" with columns:
      | column   | description |
      | order_id |             |
    And the file "models/_sources.yml":
      """
      version: 2
      sources:
        - name: crm
          tables:
            - name: orders
              columns:
                - name: order_id
      """

  Scenario: an undocumented source column takes the wording from downstream
    Given a model "stg_orders" reading from source "crm.orders" with columns:
      | column   | description             |
      | order_id | Identifier of an order. |
    And the config:
      """
      inheritance:
        backfill:
          enabled: true
      """
    When dbt-ditto runs
    Then the file "models/_sources.yml" contains:
      """
      description: Identifier of an order.
      """

  Scenario: backfill is off by default
    Given a model "stg_orders" reading from source "crm.orders" with columns:
      | column   | description             |
      | order_id | Identifier of an order. |
    When dbt-ditto runs
    Then the file "models/_sources.yml" does not contain "Identifier of an order."
