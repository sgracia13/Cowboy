from billing.totals import total

def test_total():
    assert total([]) == 0
