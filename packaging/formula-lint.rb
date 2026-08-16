# D6 — lint a rendered Homebrew formula WITHOUT Homebrew.
#
#   ruby packaging/formula-lint.rb dist/cairn.rb
#
# `brew audit` / `brew style` need a Homebrew installation, and CI here should
# be able to say something true about the formula on a runner that has none —
# and so should a developer. So this stubs just enough of the formula DSL to
# EVALUATE the class body, then resolves it for each platform we ship and
# checks what came out.
#
# What it catches: syntax errors, a DSL method that does not exist (a typo in
# `sha256` or `on_intel` is silent to `ruby -c`), a platform whose url or
# checksum never got set, and — the one that matters most — a placeholder that
# survived rendering, or a checksum that is not a real sha256. A formula with a
# wrong sha256 fails at install time on a user's machine; here it fails in CI.
#
# What it does NOT catch: anything about Homebrew's own conventions or whether
# `brew install` actually works. Only a machine with Homebrew can say that.

require "set"

PLATFORMS = [
  { os: :macos, arch: :arm,   label: "macos/arm64",  artifact: "darwin_arm64" },
  { os: :macos, arch: :intel, label: "macos/x86_64", artifact: nil },
  { os: :linux, arch: :intel, label: "linux/x86_64", artifact: "linux_amd64" },
  { os: :linux, arch: :arm,   label: "linux/arm64",  artifact: "linux_arm64" },
].freeze

# State lives outside the class: the formula body defines its own subclass
# (`class Cairn < Formula`), so per-class instance variables would land on
# whichever class happens to be evaluating. One shared record keeps it simple.
STATE = { os: nil, arch: nil, fields: {}, deps: [] }

def reset_state!(os, arch)
  STATE[:os] = os
  STATE[:arch] = arch
  STATE[:fields] = {}
  STATE[:deps] = []
end

class FormulaStub
  class << self
    %i[desc homepage version license url sha256 mirror head].each do |name|
      define_method(name) { |value| STATE[:fields][name] = value }
    end

    def depends_on(value) = STATE[:deps] << value

    def on_macos(&blk) = (instance_eval(&blk) if STATE[:os] == :macos)
    def on_linux(&blk) = (instance_eval(&blk) if STATE[:os] == :linux)
    def on_arm(&blk)   = (instance_eval(&blk) if STATE[:arch] == :arm)
    def on_intel(&blk) = (instance_eval(&blk) if STATE[:arch] == :intel)

    # Bodies we only need to accept, not run.
    def test(&blk); end
    def service(&blk); end
    def livecheck(&blk); end
  end
end

path = ARGV[0]
abort "usage: ruby packaging/formula-lint.rb <rendered-formula.rb>" if path.nil?
abort "not found: #{path}" unless File.file?(path)

# Explicit UTF-8: the formula's prose contains em-dashes, and under a C locale
# Ruby would otherwise read it as US-ASCII and `eval` would die on the first
# multibyte character — a lint failure that says nothing about the formula.
source = File.read(path, encoding: "UTF-8")
errors = []

if source.include?("@@")
  errors << "unsubstituted template placeholder(s): " \
            "#{source.scan(/@@[A-Z_0-9]+@@/).to_set.to_a.join(", ")}"
end

seen_sums = {}

PLATFORMS.each do |p|
  # The formula body runs once per platform, with different on_os/on_arch
  # answers — which is what Homebrew effectively does. Both constants are
  # dropped first: re-opening `class Cairn < Formula` against a different
  # superclass object is a TypeError.
  Object.send(:remove_const, :Cairn) if Object.const_defined?(:Cairn)
  Object.send(:remove_const, :Formula) if Object.const_defined?(:Formula)
  Object.const_set(:Formula, Class.new(FormulaStub))
  reset_state!(p[:os], p[:arch])

  begin
    # Only the class body matters; instance methods (install/caveats) reference
    # Homebrew helpers we deliberately do not stub, and are never called here.
    eval(source, TOPLEVEL_BINDING, path) # rubocop:disable Security/Eval
  rescue StandardError, ScriptError => e
    errors << "#{p[:label]}: formula body failed to evaluate: #{e.class}: #{e.message}"
    next
  end

  fields = STATE[:fields]

  if p[:artifact].nil?
    # Unsupported platform: no url is CORRECT, but there must be a requirement
    # that fails loudly rather than a formula that silently has nothing to do.
    if fields[:url]
      errors << "#{p[:label]}: has a url, but no artifact is built for it"
    elsif STATE[:deps].none? { |d| d.is_a?(Hash) && d.key?(:arch) }
      errors << "#{p[:label]}: unsupported, but nothing declares why — " \
                "add `depends_on arch:` so the failure names the reason"
    end
    next
  end

  url = fields[:url].to_s
  sum = fields[:sha256].to_s

  errors << "#{p[:label]}: no url" if url.empty?
  unless url.include?(p[:artifact])
    errors << "#{p[:label]}: url does not name the #{p[:artifact]} artifact: #{url}"
  end
  unless url.start_with?("https://")
    errors << "#{p[:label]}: url is not https: #{url}"
  end
  unless sum =~ /\A[0-9a-f]{64}\z/
    errors << "#{p[:label]}: sha256 is not a 64-hex-digit checksum: #{sum.inspect}"
  end
  if seen_sums.key?(sum) && !sum.empty?
    errors << "#{p[:label]}: shares a checksum with #{seen_sums[sum]} — " \
              "two platforms cannot have identical artifacts"
  end
  seen_sums[sum] = p[:label]

  %i[desc homepage version license].each do |k|
    errors << "#{p[:label]}: missing #{k}" if fields[k].to_s.empty?
  end
end

unless source.include?("test do")
  errors << "formula has no `test do` block — the installed binary would never be exercised"
end

if errors.empty?
  puts "formula-lint OK: #{path}"
else
  warn "formula-lint FAILED: #{path}"
  errors.each { |e| warn "  - #{e}" }
  exit 1
end
