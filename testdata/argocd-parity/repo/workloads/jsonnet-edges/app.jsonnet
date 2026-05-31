local common = import 'configmap.libsonnet';

function(namespace, flavor)
  common.configMap(std.extVar('cmName'), namespace, {
    source: std.extVar('source'),
    flavor: flavor,
    library: common.library,
  })
