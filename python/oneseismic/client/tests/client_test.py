import msgpack
import numpy as np
import numpy.testing as npt
import pytest
import requests
import requests_mock

from ..client import http_session
from ..client import cube
from ..client import cubes

session = requests.Session()
adapter = requests_mock.Adapter()
session.mount('mock://', adapter)

slice_0_12 = msgpack.packb([
    {
        'index': [
            list(range(3)),
            list(range(2)),
        ],
    },
    [
        {
            'shape': [3, 2],
            'tiles': [
                {
                    'initial-skip': 0,
                    'chunk-size': 2,
                    'iterations': 3,
                    'substride': 2,
                    'superstride': 2,
                    "v": [2.00, 2.01, 2.10, 2.11, 2.20, 2.21],
                },
            ],
        },
    ],
])

slice_1_22 = msgpack.packb([
    {
        'index': [
            list(range(4)),
            list(range(2)),
        ],
    },
    [
        {
            'shape': [4, 2],
            'tiles': [
                {
                    'initial-skip': 0,
                    'chunk-size': 2,
                    'iterations': 3,
                    'substride': 2,
                    'superstride': 2,
                    'v': [0.20, 0.21, 1.20, 1.21, 2.20, 2.21],
                },
                {
                    'initial-skip': 6,
                    'chunk-size': 2,
                    'iterations': 1,
                    'substride': 2,
                    'superstride': 2,
                    'v': [3.20, 3.21],
                },
            ],
        },
    ],
])

slice_2_30 = msgpack.packb([
    {
        'index': [
            list(range(4)),
            list(range(3)),
        ],
    },
    [
        {
            'shape': [4, 3],
            'tiles': [
                {
                    'initial-skip': 0,
                    'chunk-size': 3,
                    'iterations': 3,
                    'substride': 3,
                    'superstride': 3,
                    'v': [0.00, 0.10, 0.20, 1.00, 1.10, 1.20, 2.00, 2.10, 2.20],
                },
            ],
        },
        {
            'shape': [4, 3],
            'tiles': [
                {
                    'initial-skip': 9,
                    'chunk-size': 3,
                    'iterations': 1,
                    'substride': 3,
                    'superstride': 3,
                    'v': [3.00, 3.10, 3.20],
                },
            ],
        },
    ],
])

session = http_session(base_url = 'http://api')
cube = cube('test_id', session)

@requests_mock.Mocker(kw='m')
def test_shape(**kwargs):
    response = '''{
        "dimensions":[
            {
                "dimension": 0,
                "location": "query/test_id/slice/0",
                "size": 4,
                "keys": [0,1,2,3]
            },
            {
                "dimension": 1,
                "location": "query/test_id/slice/1",
                "size": 3,
                "keys": [0,2,4]
            },
            {
                "dimension": 2,
                "location": "query/test_id/slice/2",
                "size":2,
                "keys": [8, 16]
            }
        ],
        "pid":"pid-test-shape"
    }'''

    kwargs['m'].get('http://api/query/test_id', text = response)
    assert cube.shape == (4, 3, 2)

@requests_mock.Mocker(kw='m')
def test_slice(**kwargs):
    pid_0_12 = '{ "location": "result/pid-0-12", "status": "result/pid-0-12/status", "authorization": "" }'
    pid_1_22 = '{ "location": "result/pid-1-22", "status": "result/pid-1-22/status", "authorization": "" }'
    pid_2_30 = '{ "location": "result/pid-2-30", "status": "result/pid-2-30/status", "authorization": "" }'

    kwargs['m'].get('http://api/query/test_id/slice/0/12', text = pid_0_12)
    kwargs['m'].get('http://api/query/test_id/slice/1/22', text = pid_1_22)
    kwargs['m'].get('http://api/query/test_id/slice/2/30', text = pid_2_30)

    status_0_12 = '{ "location": "result/pid-0-12", "status": "result/pid-0-12/status" }'
    status_1_22 = '{ "location": "result/pid-1-22", "status": "result/pid-1-22/status" }'
    status_2_30 = '{ "location": "result/pid-2-30", "status": "result/pid-2-30/status" }'
    kwargs['m'].get('http://api/result/pid-0-12/status', text = status_0_12)
    kwargs['m'].get('http://api/result/pid-1-22/status', text = status_1_22)
    kwargs['m'].get('http://api/result/pid-2-30/status', text = status_2_30)

    kwargs['m'].get('http://api/result/pid-0-12/stream', content = slice_0_12)
    kwargs['m'].get('http://api/result/pid-1-22/stream', content = slice_1_22)
    kwargs['m'].get('http://api/result/pid-2-30/stream', content = slice_2_30)

    expected_0_12 = np.asarray(
        [
            [2.00, 2.01],
            [2.10, 2.11],
            [2.20, 2.21]
        ], dtype = 'single'
    )

    expected_1_22 = np.asarray(
        [
            [0.20, 0.21],
            [1.20, 1.21],
            [2.20, 2.21],
            [3.20, 3.21]
        ], dtype = 'single'
    )

    expected_2_30 = np.asarray(
        [
            [0.00, 0.10, 0.20],
            [1.00, 1.10, 1.20],
            [2.00, 2.10, 2.20],
            [3.00, 3.10, 3.20],
        ], dtype = 'single'
    )

    print(cube.slice(0, 12).numpy() - expected_0_12)
    npt.assert_array_equal(cube.slice(0, 12).numpy(), expected_0_12)
    npt.assert_array_equal(cube.slice(1, 22).numpy(), expected_1_22)
    npt.assert_array_equal(cube.slice(2, 30).numpy(), expected_2_30)

@requests_mock.Mocker(kw='m')
def test_ls(**kwargs):
    from ..ls import ls
    response = '''
    {
        "links": {
            "key1": "query/key1",
            "key2": "query/key2",
            "key3": "query/key3"
        }
    }
    '''
    kwargs['m'].get('http://api/query', text = response)
    keys = ls(session)
    assert list(keys) == ['key1', 'key2', 'key3']

@requests_mock.Mocker(kw='m')
def test_cubes_dictlike(**kwargs):
    response = '''
    {
        "links": {
            "key1": "query/key1",
            "key2": "query/key2",
            "key3": "query/key3"
        }
    }
    '''
    kwargs['m'].get('http://api/query', text = response)

    expected = ['key1', 'key2', 'key3']

    c = cubes(session)
    assert [guid for guid in c] == expected
    assert len(c) == len(expected)

    for guid in expected:
        cube = c[guid]
        assert cube.guid == guid

    _ = c.items()
    _ = c.keys()
    _ = c.items()

    with pytest.raises(KeyError):
        _ = c['no-such-key']
