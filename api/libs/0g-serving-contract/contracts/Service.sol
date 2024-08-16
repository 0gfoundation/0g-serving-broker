// SPDX-License-Identifier: Unlicense
pragma solidity >=0.8.0 <0.9.0;

import "@openzeppelin/contracts/utils/structs/EnumerableSet.sol";

struct Service {
    address provider;
    string name;
    string serviceType;
    string url;
    uint inputPrice;
    uint outputPrice;
    uint updatedAt;
}

library ServiceLibrary {
    using EnumerableSet for EnumerableSet.Bytes32Set;

    error ServiceNotexist(address provider, string name);

    struct ServiceMap {
        EnumerableSet.Bytes32Set _keys;
        mapping(bytes32 => Service) _values;
    }

    function getService(
        ServiceMap storage map,
        address provider,
        string memory name
    ) internal view returns (Service storage) {
        return _get(map, provider, name);
    }

    function getAllServices(ServiceMap storage map) internal view returns (Service[] memory services) {
        uint len = _length(map);
        services = new Service[](len);
        for (uint i = 0; i < len; ++i) {
            services[i] = _at(map, i);
        }
    }

    function addOrUpdateService(
        ServiceMap storage map,
        address provider,
        string memory name,
        string memory serviceType,
        string memory url,
        uint inputPrice,
        uint outputPrice
    ) internal {
        bytes32 key = _key(provider, name);
        if (!_contains(map, key)) {
            _set(map, key, Service(provider, name, serviceType, url, inputPrice, outputPrice, block.timestamp));
            return;
        }
        Service storage value = _get(map, provider, name);
        value.name = name;
        value.serviceType = serviceType;
        value.inputPrice = inputPrice;
        value.outputPrice = outputPrice;
        value.url = url;
        value.updatedAt = block.timestamp;
    }

    function removeService(ServiceMap storage map, address provider, string memory name) internal {
        bytes32 key = _key(provider, name);
        if (!_contains(map, key)) {
            revert ServiceNotexist(provider, name);
        }
        _remove(map, key);
    }

    function _at(ServiceMap storage map, uint index) internal view returns (Service storage) {
        bytes32 key = map._keys.at(index);
        return map._values[key];
    }

    function _set(ServiceMap storage map, bytes32 key, Service memory value) internal returns (bool) {
        map._values[key] = value;
        return map._keys.add(key);
    }

    function _get(
        ServiceMap storage map,
        address provider,
        string memory name
    ) internal view returns (Service storage) {
        bytes32 key = _key(provider, name);
        Service storage value = map._values[key];
        if (!_contains(map, key)) {
            revert ServiceNotexist(provider, name);
        }
        return value;
    }

    function _remove(ServiceMap storage map, bytes32 key) internal returns (bool) {
        delete map._values[key];
        return map._keys.remove(key);
    }

    function _contains(ServiceMap storage map, bytes32 key) internal view returns (bool) {
        return map._keys.contains(key);
    }

    function _length(ServiceMap storage map) internal view returns (uint) {
        return map._keys.length();
    }

    function _key(address provider, string memory name) internal pure returns (bytes32) {
        return keccak256(abi.encode(provider, name));
    }
}
