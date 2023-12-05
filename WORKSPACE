load("@bazel_tools//tools/build_defs/repo:http.bzl", "http_archive")

http_archive(
    name = "googleapis",
    sha256 = "9d1a930e767c93c825398b8f8692eca3fe353b9aaadedfbcf1fca2282c85df88",
    strip_prefix = "googleapis-64926d52febbf298cb82a8f472ade4a3969ba922",
    urls = [
        "https://github.com/googleapis/googleapis/archive/64926d52febbf298cb82a8f472ade4a3969ba922.zip",
    ],
)

load("@googleapis//:repository_rules.bzl", "switched_rules_by_language")

switched_rules_by_language(
    name = "com_google_googleapis_imports",
)

http_archive(
    name = "io_bazel_rules_go",
    sha256 = "278b7ff5a826f3dc10f04feaf0b70d48b68748ccd512d7f98bf442077f043fe3",
    urls = [
        "https://mirror.bazel.build/github.com/bazelbuild/rules_go/releases/download/v0.41.0/rules_go-v0.41.0.zip",
        "https://github.com/bazelbuild/rules_go/releases/download/v0.41.0/rules_go-v0.41.0.zip",
    ],
)

load("@io_bazel_rules_go//go:deps.bzl", "go_register_toolchains", "go_rules_dependencies")

http_archive(
    name = "bazel_gazelle",
    sha256 = "d3fa66a39028e97d76f9e2db8f1b0c11c099e8e01bf363a923074784e451f809",
    urls = [
        "https://mirror.bazel.build/github.com/bazelbuild/bazel-gazelle/releases/download/v0.33.0/bazel-gazelle-v0.33.0.tar.gz",
        "https://github.com/bazelbuild/bazel-gazelle/releases/download/v0.33.0/bazel-gazelle-v0.33.0.tar.gz",
    ],
)

load("@bazel_gazelle//:deps.bzl", "gazelle_dependencies", "go_repository")

go_rules_dependencies()

go_register_toolchains(version = "1.21.5")

http_archive(
    name = "rules_proto",
    sha256 = "dc3fb206a2cb3441b485eb1e423165b231235a1ea9b031b4433cf7bc1fa460dd",
    strip_prefix = "rules_proto-5.3.0-21.7",
    urls = [
        "https://github.com/bazelbuild/rules_proto/archive/refs/tags/5.3.0-21.7.tar.gz",
    ],
)

load("@rules_proto//proto:repositories.bzl", "rules_proto_dependencies", "rules_proto_toolchains")

rules_proto_dependencies()

rules_proto_toolchains()

load("//:deps.bzl", "go_dependencies")

# gazelle:repository_macro deps.bzl%go_dependencies
go_dependencies()

go_repository(
    name = "com_github_aws_aws_sdk_go",
    importpath = "github.com/aws/aws-sdk-go",
    sum = "h1:O8VH+bJqgLDguqkH/xQBFz5o/YheeZqgcOYIgsTVWY4=",
    version = "v1.44.256",
)

go_repository(
    name = "com_github_jmespath_go_jmespath",
    importpath = "github.com/jmespath/go-jmespath",
    sum = "h1:BEgLn5cpjn8UN1mAw4NjwDrS35OdebyEtFe+9YPoQUg=",
    version = "v0.4.0",
)

go_repository(
    name = "com_github_jmespath_go_jmespath_internal_testify",
    importpath = "github.com/jmespath/go-jmespath/internal/testify",
    sum = "h1:shLQSRRSCCPj3f2gpwzGwWFoC7ycTf1rcQZHOlsJ6N8=",
    version = "v1.5.1",
)

go_repository(
    name = "com_github_johannesboyne_gofakes3",
    importpath = "github.com/johannesboyne/gofakes3",
    sum = "h1:O7syWuYGzre3s73s+NkgB8e0ZvsIVhT/zxNU7V1gHK8=",
    version = "v0.0.0-20230506070712-04da935ef877",
)

go_repository(
    name = "com_github_ryszard_goskiplist",
    importpath = "github.com/ryszard/goskiplist",
    sum = "h1:GHRpF1pTW19a8tTFrMLUcfWwyC0pnifVo2ClaLq+hP8=",
    version = "v0.0.0-20150312221310-2dfbae5fcf46",
)

go_repository(
    name = "com_github_shabbyrobe_gocovmerge",
    importpath = "github.com/shabbyrobe/gocovmerge",
    sum = "h1:WnNuhiq+FOY3jNj6JXFT+eLN3CQ/oPIsDPRanvwsmbI=",
    version = "v0.0.0-20190829150210-3e036491d500",
)

go_repository(
    name = "com_github_spf13_afero",
    importpath = "github.com/spf13/afero",
    sum = "h1:qgMbHoJbPbw579P+1zVY+6n4nIFuIchaIjzZ/I/Yq8M=",
    version = "v1.2.1",
)

go_repository(
    name = "in_gopkg_mgo_v2",
    importpath = "gopkg.in/mgo.v2",
    sum = "h1:xcEWjVhvbDy+nHP67nPDDpbYrY+ILlfndk4bRioVHaU=",
    version = "v2.0.0-20180705113604-9856a29383ce",
)

go_repository(
    name = "io_etcd_go_bbolt",
    importpath = "go.etcd.io/bbolt",
    sum = "h1:XAzx9gjCb0Rxj7EoqcClPD1d5ZBxZJk0jbuoPHenBt0=",
    version = "v1.3.5",
)

gazelle_dependencies()

http_archive(
    name = "io_bazel_rules_docker",
    # Waiting for the next release after v0.25.0 ...
    sha256 = "9cdc7ac9f19fa3ad49cf9ba9f50652f0067df5c347dafdbdfe84c6c37a8ed62b",
    strip_prefix = "rules_docker-48ad6d6df43d1e4b9feeec961995aef01dd72080",
    urls = ["https://github.com/bazelbuild/rules_docker/archive/48ad6d6df43d1e4b9feeec961995aef01dd72080.tar.gz"],
)

load(
    "@io_bazel_rules_docker//repositories:repositories.bzl",
    container_repositories = "repositories",
)

container_repositories()

load("@io_bazel_rules_docker//repositories:deps.bzl", container_deps = "deps")

container_deps()

load(
    "@io_bazel_rules_docker//container:container.bzl",
    "container_pull",
)

container_pull(
    name = "cgo_amd64_base",
    registry = "gcr.io",
    repository = "distroless/base-nossl-debian11",
    # See https://github.com/buchgr/bazel-remote/issues/605 and https://github.com/GoogleContainerTools/distroless/issues/1098
    # TODO: specify this by digest instead? Where can I find that?
    tag = "nonroot-amd64",
)

container_pull(
    name = "cgo_arm64_base",
    registry = "gcr.io",
    repository = "distroless/base-nossl-debian11",
    tag = "nonroot-arm64",
)

load(
    "@io_bazel_rules_docker//go:image.bzl",
    _go_image_repos = "repositories",
)

_go_image_repos()
