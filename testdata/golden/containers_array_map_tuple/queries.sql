-- name: ReadContainers :one
SELECT
    tags   AS tag_list,
    nested AS nested_list,
    attrs  AS attribute_map
FROM container_probe
WHERE id = chgen.arg('ID');

-- Tuple element access, by position and by name. A Tuple is readable one
-- element at a time; the whole Tuple is not, see refuse_whole_tuple_result.
-- name: ReadTupleElements :one
SELECT
    pair.1                   AS first_element,
    pair.2                   AS second_element,
    named.a                  AS named_element,
    tupleElement(named, 'b') AS named_by_function
FROM container_probe
WHERE id = chgen.arg('ID');
