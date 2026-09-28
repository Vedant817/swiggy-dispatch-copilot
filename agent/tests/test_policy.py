from app.policy import check_commit, validate_ids_exist


def test_commit_policy():
    ok, _ = check_commit({"order": {"status": "offering"}}, "r1", {"r1"})
    assert ok
    ok, _ = check_commit({"order": {"status": "assigned"}}, "r1", {"r1"})
    assert not ok
    ok, _ = check_commit({"order": {"status": "offering"}}, "r2", {"r1"})
    assert not ok


def test_invented_ids():
    ok, _ = validate_ids_exist("o1", "r1", {"o1"}, {"r1"})
    assert ok
    ok, _ = validate_ids_exist("oX", "r1", {"o1"}, {"r1"})
    assert not ok
