Feature: Output does not depend on the order dbt listed its nodes in
  dbt makes no promise about the order of the nodes in a manifest: two parses of
  the same project can list two models either way round, and the order differs
  between machines. dbt-ditto lays entries out in manifest order because
  dbt-osmosis does, so that order is visible in the bytes whenever several nodes
  share one schema file.

  What must never differ is the documentation itself. Canonicalising a tree —
  sorting the named entries, which is what scripts/parity.sh does to both sides
  before diffing — has to make two runs over the same project identical, however
  dbt happened to order the manifest.

  Background:
    Given a dbt project
    And a seed "raw_orders" with columns:
      | column   | description             |
      | order_id | Identifier of an order. |
    And a seed "raw_regions" with columns:
      | column      | description       |
      | region_code | ISO region code.  |

  Scenario: two manifest orders document the same thing
    Given a model "stg" reading from "raw_orders" with columns:
      | column   | description |
      | order_id |             |
    When dbt-ditto runs
    And dbt-ditto runs again with the manifest nodes in reverse order
    Then the two runs are identical once canonicalised

  Scenario: entries sharing one file follow manifest order
    When dbt-ditto runs
    Then the file "seeds/_seeds.yml" lists entries in the order:
      | name        |
      | raw_orders  |
      | raw_regions |
    When dbt-ditto runs again with the manifest nodes in reverse order
    Then the file "seeds/_seeds.yml" lists entries in the order:
      | name        |
      | raw_regions |
      | raw_orders  |
