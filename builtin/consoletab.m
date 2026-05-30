// consoletab.m – injects a "Console" tab into the VM graphical window.
//
// After NSApplicationDidFinishLaunchingNotification fires (i.e. after
// startVirtualMachineWindow has set up the VZVirtualMachineView window),
// we create a second NSWindow containing a scrolling terminal-style text view
// that tails the VM's serial console log file, then attach it as a native
// macOS tab using addTabbedWindow:ordered:.
//
// The console window is styled to exactly match the VZ graphical window:
//   - titlebarAppearsTransparent = YES
//   - opaque = NO
//   - same NSToolbar as the GPU tab: we copy the toolbar reference from
//     the main window so every button and action is identical.

#import <Cocoa/Cocoa.h>
#import <objc/runtime.h>

// goVMReboot is exported by the Go side (consoletab.go) and performs an
// in-process VM stop followed by a restart.
extern void goVMReboot(void);

// Identifier used for the Reboot toolbar item injected into all VM windows.
static NSString *const RebootToolbarIdentifier = @"MockReboot";

// ---------------------------------------------------------------------------
// MockRebootTarget – singleton action target for the Reboot toolbar item.
// Using a dedicated object (rather than nil / first-responder chain) ensures
// the button is always enabled and the action always reaches goVMReboot()
// regardless of which tab (GPU or Console) is currently active.
// ---------------------------------------------------------------------------
@interface MockRebootTarget : NSObject
+ (instancetype)shared;
- (void)rebootVM:(id)sender;
@end
@implementation MockRebootTarget
+ (instancetype)shared {
    static MockRebootTarget *s;
    static dispatch_once_t once;
    dispatch_once(&once, ^{ s = [MockRebootTarget new]; });
    return s;
}
- (void)rebootVM:(id)sender { goVMReboot(); }
@end

// ---------------------------------------------------------------------------
// MockScrollView – NSScrollView subclass that always reports NSScrollerStyleLegacy
// so the system preference ("Show scroll bars: Automatically") cannot override it.
// ---------------------------------------------------------------------------
@interface MockScrollView : NSScrollView
@end
@implementation MockScrollView
- (NSScrollerStyle)scrollerStyle { return NSScrollerStyleLegacy; }
- (void)setScrollerStyle:(NSScrollerStyle)s {
    // Ignore the requested style; always use Legacy (permanently visible scrollers).
    [super setScrollerStyle:NSScrollerStyleLegacy];
}
- (void)tile {
    // Force Legacy style before AppKit computes the content-view frame so that
    // the scrollbar track is always reserved and permanently visible.
    [super setScrollerStyle:NSScrollerStyleLegacy];
    [super tile];
}
@end

// ---------------------------------------------------------------------------
// MockConsoleViewController – black terminal view that polls the console log
// file every 200 ms and appends new bytes as green monospaced text.
// ---------------------------------------------------------------------------
@interface MockConsoleViewController : NSViewController
@property (nonatomic, copy)   NSString      *consolePath;
@property (nonatomic, strong) NSTextView    *textView;
@property (nonatomic, strong) MockScrollView *scrollView;
@property (nonatomic)         unsigned long long lastReadOffset;
@end

@implementation MockConsoleViewController

// Strip ANSI/VT100 escape sequences from a string before display.
// Handles:
//   ESC [ ... <final>   (CSI sequences: cursor movement, colours, etc.)
//   ESC ] ... BEL/ST    (OSC sequences: window title, etc.)
//   ESC <single char>   (two-character sequences)
static NSString *stripAnsiEscapes(NSString *s) {
    static NSRegularExpression *re;
    static dispatch_once_t once;
    dispatch_once(&once, ^{
        // \x1b[  + params + final byte   (CSI)
        // \x1b]  + any   + BEL or ESC\  (OSC)
        // \x1b   + single non-[ char     (2-char escape)
        NSString *pattern = @"\\x1b(?:\\[[0-?]*[ -/]*[@-~]|\\][^\\x07\\x1b]*(?:\\x07|\\x1b\\\\)|[@-Z\\\\-_])";
        re = [NSRegularExpression regularExpressionWithPattern:pattern
                                                       options:0
                                                         error:nil];
    });
    NSRange all = NSMakeRange(0, s.length);
    return [re stringByReplacingMatchesInString:s options:0 range:all withTemplate:@""];
}

- (void)loadView {
    NSRect frame = NSMakeRect(0, 0, 1280, 800);
    MockScrollView *scroll = [[MockScrollView alloc] initWithFrame:frame];
    scroll.hasVerticalScroller   = YES;
    scroll.autoresizingMask      = NSViewWidthSizable | NSViewHeightSizable;
    scroll.scrollerKnobStyle     = NSScrollerKnobStyleLight;
    scroll.autohidesScrollers    = NO;
    scroll.drawsBackground       = YES;
    scroll.backgroundColor       = [NSColor blackColor];
    // Force the vertical scroller itself to Legacy style so AppKit does not
    // replace it with an overlay indicator regardless of the system preference.
    scroll.verticalScroller.scrollerStyle = NSScrollerStyleLegacy;

    NSTextView *tv = [[NSTextView alloc] initWithFrame:scroll.contentView.bounds];
    tv.editable             = NO;
    tv.selectable           = YES;
    tv.richText             = NO;
    tv.backgroundColor      = [NSColor blackColor];
    tv.insertionPointColor  = [NSColor greenColor];
    tv.font                 = [NSFont monospacedSystemFontOfSize:12.0
                                                         weight:NSFontWeightRegular];
    tv.textColor            = [NSColor colorWithRed:0.2 green:0.9 blue:0.2 alpha:1.0];
    tv.textContainerInset   = NSZeroSize;
    tv.autoresizingMask     = NSViewWidthSizable;
    tv.maxSize              = NSMakeSize(FLT_MAX, FLT_MAX);
    tv.minSize              = NSMakeSize(0.0, 0.0);
    [tv.textContainer setWidthTracksTextView:YES];
    [tv.textContainer setHeightTracksTextView:NO];

    scroll.documentView = tv;
    self.textView  = tv;
    self.scrollView = scroll;
    self.view = scroll;
}

- (void)viewDidAppear {
    [super viewDidAppear];
    // Force the scroller to re-read scrollerStyle=Legacy and re-tile now that
    // we are in a live window (tile earlier has no effect without a window).
    [self.scrollView tile];
    // Start the polling timer once we are on screen.
    [NSTimer scheduledTimerWithTimeInterval:0.2
                                     target:self
                                   selector:@selector(pollConsole:)
                                   userInfo:nil
                                    repeats:YES];
    // Immediately do a first read so existing content shows up at once.
    [self pollConsole:nil];
}

- (void)pollConsole:(NSTimer *)timer {
    NSFileHandle *fh = [NSFileHandle fileHandleForReadingAtPath:self.consolePath];
    if (!fh) return;
    [fh seekToFileOffset:self.lastReadOffset];
    NSData *data = [fh readDataToEndOfFile];
    [fh closeFile];
    if (data.length == 0) return;
    self.lastReadOffset += data.length;

    // Try UTF-8 first, fall back to Latin-1 for raw byte streams.
    NSString *str = [[NSString alloc] initWithData:data encoding:NSUTF8StringEncoding];
    if (!str) {
        str = [[NSString alloc] initWithData:data encoding:NSISOLatin1StringEncoding];
    }
    if (!str) return;
    str = stripAnsiEscapes(str);
    if (str.length == 0) return;

    NSDictionary *attrs = @{
        NSForegroundColorAttributeName: [NSColor colorWithRed:0.2 green:0.9 blue:0.2 alpha:1.0],
        NSFontAttributeName:            [NSFont monospacedSystemFontOfSize:12.0
                                                                    weight:NSFontWeightRegular],
    };
    NSAttributedString *as = [[NSAttributedString alloc] initWithString:str attributes:attrs];

    dispatch_async(dispatch_get_main_queue(), ^{
        BOOL atBottom = [self isScrolledToBottom];
        [self.textView.textStorage appendAttributedString:as];
        if (atBottom) {
            [self.textView scrollToEndOfDocument:nil];
        }
    });
}

- (BOOL)isScrolledToBottom {
    NSScrollView *sv = self.scrollView;
    NSClipView  *cv = sv.contentView;
    CGFloat maxY = sv.documentView.frame.size.height - cv.bounds.size.height;
    return (cv.bounds.origin.y >= maxY - 2.0);
}

@end

// ---------------------------------------------------------------------------
// Toolbar swizzle – injects a "Reboot" item into the VZ AppDelegate toolbar.
//
// The VZ library manages toolbar items internally via a private
// -setToolBarItems: method that bypasses the delegate's
// toolbarDefaultItemIdentifiers:. Swizzling all four relevant methods ensures
// the Reboot button:
//   • appears immediately in the GPU window toolbar,
//   • survives every toolbar rebuild triggered by VM state transitions, and
//   • is also present in the Console tab toolbar (populated via delegate).
// ---------------------------------------------------------------------------
static void (*orig_setToolBarItems)(id, SEL, NSArray *);
static NSArray *(*orig_toolbarDefaultItemIdentifiers)(id, SEL, NSToolbar *);
static NSArray *(*orig_toolbarAllowedItemIdentifiers)(id, SEL, NSToolbar *);
static NSToolbarItem *(*orig_toolbarItemForIdentifier)(id, SEL, NSToolbar *, NSToolbarItemIdentifier, BOOL);

static void swizzled_setToolBarItems(id self, SEL _cmd, NSArray<NSToolbarItemIdentifier> *items) {
    NSMutableArray *modified = [items mutableCopy];
    if (![modified containsObject:RebootToolbarIdentifier]) {
        [modified insertObject:RebootToolbarIdentifier atIndex:0];
    }
    orig_setToolBarItems(self, _cmd, modified);
}

static NSArray *swizzled_toolbarDefaultItemIdentifiers(id self, SEL _cmd, NSToolbar *toolbar) {
    NSMutableArray *items = [orig_toolbarDefaultItemIdentifiers(self, _cmd, toolbar) mutableCopy];
    if (![items containsObject:RebootToolbarIdentifier]) {
        [items insertObject:RebootToolbarIdentifier atIndex:0];
    }
    return items;
}

static NSArray *swizzled_toolbarAllowedItemIdentifiers(id self, SEL _cmd, NSToolbar *toolbar) {
    NSMutableArray *items = [orig_toolbarAllowedItemIdentifiers(self, _cmd, toolbar) mutableCopy];
    if (![items containsObject:RebootToolbarIdentifier]) {
        [items insertObject:RebootToolbarIdentifier atIndex:0];
    }
    return items;
}

static NSToolbarItem *swizzled_toolbarItemForIdentifier(id self, SEL _cmd,
                                                        NSToolbar *toolbar,
                                                        NSToolbarItemIdentifier ident,
                                                        BOOL flag) {
    if ([ident isEqualToString:RebootToolbarIdentifier]) {
        NSToolbarItem *item = [[NSToolbarItem alloc] initWithItemIdentifier:RebootToolbarIdentifier];
        item.image    = [NSImage imageWithSystemSymbolName:@"arrow.counterclockwise"
                                    accessibilityDescription:@"Reboot VM"];
        item.label    = @"Reboot";
        item.toolTip  = @"Reboot VM";
        item.bordered = YES;
        item.target   = [MockRebootTarget shared];
        item.action   = @selector(rebootVM:);
        return item;
    }
    return orig_toolbarItemForIdentifier(self, _cmd, toolbar, ident, flag);
}

static void installRebootToolbarSwizzle(void) {
    static dispatch_once_t once;
    dispatch_once(&once, ^{
        Class cls = NSClassFromString(@"AppDelegate");
        if (!cls) return;

        SEL s1 = NSSelectorFromString(@"setToolBarItems:");
        Method m1 = class_getInstanceMethod(cls, s1);
        if (m1) {
            orig_setToolBarItems = (void (*)(id, SEL, NSArray *))method_getImplementation(m1);
            method_setImplementation(m1, (IMP)swizzled_setToolBarItems);
        }

        SEL s2 = @selector(toolbarDefaultItemIdentifiers:);
        Method m2 = class_getInstanceMethod(cls, s2);
        if (m2) {
            orig_toolbarDefaultItemIdentifiers =
                (NSArray *(*)(id, SEL, NSToolbar *))method_getImplementation(m2);
            method_setImplementation(m2, (IMP)swizzled_toolbarDefaultItemIdentifiers);
        }

        SEL s3 = @selector(toolbarAllowedItemIdentifiers:);
        Method m3 = class_getInstanceMethod(cls, s3);
        if (m3) {
            orig_toolbarAllowedItemIdentifiers =
                (NSArray *(*)(id, SEL, NSToolbar *))method_getImplementation(m3);
            method_setImplementation(m3, (IMP)swizzled_toolbarAllowedItemIdentifiers);
        }

        SEL s4 = @selector(toolbar:itemForItemIdentifier:willBeInsertedIntoToolbar:);
        Method m4 = class_getInstanceMethod(cls, s4);
        if (m4) {
            orig_toolbarItemForIdentifier =
                (NSToolbarItem *(*)(id, SEL, NSToolbar *, NSToolbarItemIdentifier, BOOL))
                    method_getImplementation(m4);
            method_setImplementation(m4, (IMP)swizzled_toolbarItemForIdentifier);
        }
    });
}

// ---------------------------------------------------------------------------
// Public C entry point – called from Go before StartGraphicApplication.
// ---------------------------------------------------------------------------

void mockRegisterConsoleTab(const char *vmName, const char *consolePath) {
    NSString *name = [NSString stringWithUTF8String:vmName];
    NSString *path = [NSString stringWithUTF8String:consolePath];

    // Swizzle the VZ AppDelegate toolbar methods before NSApp launches so the
    // Reboot item is injected into every toolbar rebuild from the first one.
    installRebootToolbarSwizzle();

    // The observer fires on the main queue after NSApp has created the VM window.
    [[NSNotificationCenter defaultCenter]
        addObserverForName:NSApplicationDidFinishLaunchingNotification
                    object:nil
                     queue:[NSOperationQueue mainQueue]
                usingBlock:^(NSNotification *note) {
        // Brief delay to ensure the VZ window is fully set up and key.
        dispatch_after(dispatch_time(DISPATCH_TIME_NOW, (int64_t)(0.15 * NSEC_PER_SEC)),
                       dispatch_get_main_queue(), ^{
            NSWindow *mainWin = NSApp.mainWindow ?: NSApp.windows.firstObject;
            if (!mainWin) return;

            // Build the console view controller.
            MockConsoleViewController *vc = [[MockConsoleViewController alloc] init];
            vc.consolePath = path;
            vc.title = [NSString stringWithFormat:@"Console: %@", name];

            // Create a window sized like the main VM window, matching its chrome
            // exactly: titlebarAppearsTransparent, opaque=NO, same toolbar height.
            NSWindow *consoleWin = [[NSWindow alloc]
                initWithContentRect:mainWin.frame
                          styleMask:(NSWindowStyleMaskTitled          |
                                     NSWindowStyleMaskClosable        |
                                     NSWindowStyleMaskMiniaturizable  |
                                     NSWindowStyleMaskResizable)
                            backing:NSBackingStoreBuffered
                              defer:NO];
            consoleWin.title = [NSString stringWithFormat:@"Console: %@", name];
            consoleWin.contentViewController = vc;
            // Set the window backgroundColor to the system-adaptive dark window
            // color so the title-bar area matches the VZ tab chrome in any appearance.
            consoleWin.backgroundColor = [NSColor windowBackgroundColor];
            NSAppearance *darkAqua = [NSAppearance appearanceNamed:NSAppearanceNameDarkAqua];
            mainWin.appearance    = darkAqua;
            consoleWin.appearance = darkAqua;
            // Make the VZ scroll view use always-visible scrollers to match the console tab.
            // We isa-swizzle the instance to MockScrollView so the scrollerStyle override
            // applies permanently (simple setScrollerStyle: gets reset by the system).
            if ([mainWin.contentView isKindOfClass:[NSScrollView class]]) {
                NSScrollView *vzScroll = (NSScrollView *)mainWin.contentView;
                object_setClass(vzScroll, [MockScrollView class]);
                vzScroll.autohidesScrollers = NO;
                // Force re-layout so AppKit picks up the new scrollerStyle=Legacy.
                [vzScroll tile];
            }
            // Transparent title bar matching the VZ window chrome.
            [consoleWin setTitlebarAppearsTransparent:YES];
            [consoleWin setOpaque:NO];
            // Give the console window the same title bar text as the GPU window so
            // the chrome looks identical when switching tabs.
            consoleWin.title = mainWin.title;

            // NSToolbar can only be attached to ONE window at a time.
            // Sharing the same toolbar object would detach it from the GPU window,
            // leaving that tab with no toolbar. Instead, create an independent toolbar
            // with a unique identifier (using the same identifier would cause AppKit
            // to conflict the two toolbars in its internal registry and NSUserDefaults,
            // wiping items from both tabs).
            NSToolbar *gpuToolbar = mainWin.toolbar;
            if (gpuToolbar) {
                // Inject Reboot into the already-built GPU toolbar immediately
                // (swizzle only covers future rebuilds via setToolBarItems:).
                BOOL hasReboot = NO;
                for (NSToolbarItem *item in gpuToolbar.items) {
                    if ([item.itemIdentifier isEqualToString:RebootToolbarIdentifier]) {
                        hasReboot = YES; break;
                    }
                }
                if (!hasReboot) {
                    [gpuToolbar insertItemWithItemIdentifier:RebootToolbarIdentifier atIndex:0];
                }

                NSString *consoleID = [gpuToolbar.identifier
                    stringByAppendingString:@"-console"];
                NSToolbar *consoleToolbar = [[NSToolbar alloc]
                    initWithIdentifier:consoleID];
                consoleToolbar.delegate              = gpuToolbar.delegate;
                consoleToolbar.displayMode           = gpuToolbar.displayMode;
                consoleToolbar.allowsUserCustomization = NO;
                consoleToolbar.autosavesConfiguration  = NO;
                [consoleWin setToolbar:consoleToolbar];

                // When the user switches to the console tab, ask AppKit to
                // re-validate all visible toolbar items (calls validateToolbarItem:
                // on each item, updating enabled-state and labels to reflect the
                // current VM state — no need to manually rebuild the item list).
                __weak NSToolbar *weakConsole = consoleToolbar;
                [[NSNotificationCenter defaultCenter]
                    addObserverForName:NSWindowDidBecomeMainNotification
                                object:consoleWin
                                 queue:[NSOperationQueue mainQueue]
                            usingBlock:^(NSNotification *n) {
                    [weakConsole validateVisibleItems];
                }];
            }

            // Force both windows into a shared tab group.
            mainWin.tabbingMode    = NSWindowTabbingModePreferred;
            consoleWin.tabbingMode = NSWindowTabbingModePreferred;
            consoleWin.tabbingIdentifier = mainWin.tabbingIdentifier;

            [mainWin addTabbedWindow:consoleWin ordered:NSWindowAbove];

            // After joining the tab group, override the tab-bar label to distinguish
            // this tab from the GPU tab while keeping the title bar text identical.
            if (consoleWin.tab) {
                consoleWin.tab.title = [NSString stringWithFormat:@"Console: %@", name];
            }
        });
    }];
}
