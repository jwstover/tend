#!/usr/bin/perl
# Wall-clock a command N times and print min / median / p95 / max in ms.
#
#   time.pl [-n N] [--stdin STR] [--first-line] [--label L] -- cmd args...
#
# --stdin STR    feed STR to the child's stdin, then close it
# --first-line   also report spawn-to-first-stdout-line (an MCP server's
#                initialize response); stdin is closed after that line so
#                the server exits on EOF and the total is spawn-to-exit
#
# Timing is done around fork/exec + waitpid from perl, the same way a
# shell or claude would see it. Exit code != 0 is reported, not hidden.
use strict;
use warnings;
use Time::HiRes qw(time);
use IPC::Open2;
use POSIX ":sys_wait_h";

my ($n, $stdin, $first, $label) = (20, undef, 0, undef);
while (@ARGV && $ARGV[0] ne '--') {
    my $a = shift @ARGV;
    if    ($a eq '-n')           { $n = shift @ARGV }
    elsif ($a eq '--stdin')      { $stdin = shift @ARGV }
    elsif ($a eq '--first-line') { $first = 1 }
    elsif ($a eq '--label')      { $label = shift @ARGV }
    else { die "unknown option $a\n" }
}
shift @ARGV;                          # the --
my @cmd = @ARGV or die "no command\n";
$label //= join(' ', @cmd);

my (@total, @firstline, $bad);
for (1 .. $n) {
    my ($out, $in);
    my $t0 = time;
    my $pid = open2($out, $in, @cmd);
    if (defined $stdin) { print $in $stdin }
    if ($first) {
        my $line = <$out>;
        push @firstline, (time - $t0) * 1000;
        die "$label: no first line\n" unless defined $line;
    }
    close $in;
    # Drain stdout so the child never blocks on a full pipe.
    local $/; my $rest = <$out>; close $out;
    waitpid($pid, 0);
    push @total, (time - $t0) * 1000;
    $bad++ if $? != 0;
}

sub stats {
    my @s = sort { $a <=> $b } @_;
    my $p = sub { $s[int($_[0] * ($#s)) ] };
    return sprintf("min %7.1f  med %7.1f  p95 %7.1f  max %7.1f",
        $s[0], $p->(0.5), $p->(0.95), $s[-1]);
}

printf "%-34s total   %s%s\n", $label, stats(@total), ($bad ? "  [$bad non-zero exits]" : "");
printf "%-34s 1st-ln  %s\n", '', stats(@firstline) if $first;
