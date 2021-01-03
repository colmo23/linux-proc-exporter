import time

x = 0
y = ""
for z in xrange(0,100000):
    print "iteration: %d" % z
    for _ in xrange(0,10000):
        x = x + 1
        y = y + "." * 1000
        time.sleep(0.00001)
