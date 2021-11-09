import time
import random


x = 0
y = ""
for z in xrange(0,100000):
    print "iteration: %d" % z
    for _ in xrange(0,10000):
        x = x + random.randint(0,len(y))
        random.seed()
        y = y + "." * 1000
        time.sleep(0.00001)
